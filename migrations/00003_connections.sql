-- +goose Up
-- Platform connections and the token vault (M3). See docs/adr/0008-0010.
--
-- oauth_grants holds one completed OAuth flow; connections holds each
-- account attached to a brand. Both are tenant tables under RLS (ADR 0006).
-- Tokens are keyring ciphertexts ("v1.<kid>.<b64>"); the generated *_key_id
-- columns find rows sealed with a given key for rotation. Token columns are
-- nullable because disconnect and data deletion wipe them.
--
-- Webhook receipts and data-deletion requests carry no tenant: the runtime
-- role may only INSERT them; reads and updates go through the narrow
-- SECURITY DEFINER functions at the end of this file.

CREATE TABLE oauth_grants (
    id                     uuid PRIMARY KEY,
    workspace_id           uuid NOT NULL,
    brand_id               uuid NOT NULL,
    provider               text NOT NULL CHECK (provider IN ('meta', 'instagram', 'linkedin', 'google')),
    provider_user_id       text NOT NULL DEFAULT '' CHECK (length(provider_user_id) <= 128),
    scopes                 text[] NOT NULL DEFAULT '{}',
    missing_scopes         text[] NOT NULL DEFAULT '{}',
    access_token_enc       text CHECK (length(access_token_enc) <= 16384),
    access_token_key_id    text GENERATED ALWAYS AS (split_part(access_token_enc, '.', 2)) STORED,
    refresh_token_enc      text CHECK (length(refresh_token_enc) <= 16384),
    refresh_token_key_id   text GENERATED ALWAYS AS (split_part(refresh_token_enc, '.', 2)) STORED,
    access_expires_at      timestamptz,
    refresh_expires_at     timestamptz,
    data_access_expires_at timestamptz, -- Meta: 90 days after the user was last active
    token_issued_at        timestamptz, -- Instagram Login: refreshable once 24 h old
    created_by             uuid NOT NULL REFERENCES users (id),
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    -- Target of the connections FK: a connection stays on its grant's brand.
    CONSTRAINT oauth_grants_brand_id UNIQUE (workspace_id, brand_id, id),
    FOREIGN KEY (workspace_id, brand_id) REFERENCES brands (workspace_id, id) ON DELETE CASCADE
);
-- Deauthorize and data deletion: grants_by_provider_user().
CREATE INDEX oauth_grants_provider_user ON oauth_grants (provider, provider_user_id);
CREATE INDEX oauth_grants_access_key ON oauth_grants (access_token_key_id) WHERE access_token_key_id IS NOT NULL;
CREATE INDEX oauth_grants_refresh_key ON oauth_grants (refresh_token_key_id) WHERE refresh_token_key_id IS NOT NULL;

CREATE TABLE connections (
    id               uuid PRIMARY KEY,
    workspace_id     uuid NOT NULL,
    brand_id         uuid NOT NULL,
    grant_id         uuid NOT NULL,
    kind             text NOT NULL CHECK (kind IN ('facebook_page', 'instagram_account', 'linkedin_org', 'youtube_channel', 'whatsapp_number')),
    external_id      text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 128),
    display_name     text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 200),
    token_enc        text CHECK (length(token_enc) <= 16384), -- Facebook Page token; NULL when the grant's token is used
    token_key_id     text GENERATED ALWAYS AS (split_part(token_enc, '.', 2)) STORED,
    status           text NOT NULL CHECK (status IN ('active', 'error', 'needs_reauth', 'disconnected')),
    token_expires_at timestamptz,
    last_checked_at  timestamptz,
    last_error       text CHECK (last_error IN ('reauth_required', 'rate_limited', 'provider_unavailable', 'rejected')),
    warned_days      smallint NOT NULL DEFAULT 0 CHECK (warned_days IN (0, 3, 14)),
    next_due_at      timestamptz, -- from core.Evaluate; NULL when parked
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    FOREIGN KEY (workspace_id, brand_id, grant_id) REFERENCES oauth_grants (workspace_id, brand_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, brand_id) REFERENCES brands (workspace_id, id) ON DELETE CASCADE
);
-- One live connection per account per workspace; another workspace (an
-- agency and its client) may connect the same account.
CREATE UNIQUE INDEX connections_live_external ON connections (workspace_id, kind, external_id) WHERE status <> 'disconnected';
-- Keyset list WHERE workspace_id = $1 AND brand_id = $2 AND id > $3 ORDER BY id
-- (EXPLAIN: Index Only Scan using connections_brand).
CREATE INDEX connections_brand ON connections (workspace_id, brand_id, id);
-- Maintenance sweep: connections_due() (EXPLAIN: Index Scan using connections_due).
CREATE INDEX connections_due ON connections (next_due_at, id) WHERE status <> 'disconnected';
-- Webhook routing across workspaces: connections_by_external().
CREATE INDEX connections_external ON connections (kind, external_id) WHERE status <> 'disconnected';
CREATE INDEX connections_token_key ON connections (token_key_id) WHERE token_key_id IS NOT NULL;

CREATE TABLE webhook_receipts (
    provider    text NOT NULL CHECK (provider IN ('meta', 'instagram', 'linkedin')),
    dedupe_key  text NOT NULL CHECK (length(dedupe_key) BETWEEN 1 AND 128),
    received_at timestamptz NOT NULL,
    PRIMARY KEY (provider, dedupe_key)
);
-- Pruning: webhook_receipts_prune().
CREATE INDEX webhook_receipts_received ON webhook_receipts (received_at);
-- Default privileges grant all DML; ingestion needs only INSERT ... ON CONFLICT DO NOTHING.
REVOKE SELECT, UPDATE, DELETE, TRUNCATE ON webhook_receipts FROM tuorps_app;

CREATE TABLE data_deletion_requests (
    confirmation_code text PRIMARY KEY CHECK (length(confirmation_code) BETWEEN 1 AND 64),
    provider          text NOT NULL CHECK (provider IN ('meta', 'instagram')),
    provider_user_id  text NOT NULL CHECK (length(provider_user_id) BETWEEN 1 AND 128),
    status            text NOT NULL CHECK (status IN ('pending', 'done')),
    requested_at      timestamptz NOT NULL,
    completed_at      timestamptz
);
REVOKE SELECT, UPDATE, DELETE, TRUNCATE ON data_deletion_requests FROM tuorps_app;

-- Row-level security -------------------------------------------------------

ALTER TABLE oauth_grants ENABLE ROW LEVEL SECURITY;
CREATE POLICY oauth_grants_tenant ON oauth_grants
    USING (workspace_id = current_workspace_id()) WITH CHECK (workspace_id = current_workspace_id());

ALTER TABLE connections ENABLE ROW LEVEL SECURITY;
CREATE POLICY connections_tenant ON connections
    USING (workspace_id = current_workspace_id()) WITH CHECK (workspace_id = current_workspace_id());

-- Cross-tenant discovery (SECURITY DEFINER, owner privileges) --------------
-- Each returns only ids; the caller then works inside that workspace's
-- scope, under RLS.

-- +goose StatementBegin
CREATE FUNCTION connections_due(p_now timestamptz, p_limit int)
RETURNS TABLE (id uuid, workspace_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = tuorps, pg_temp
AS $$
    SELECT c.id, c.workspace_id
    FROM connections c
    JOIN brands b ON b.workspace_id = c.workspace_id AND b.id = c.brand_id
    WHERE c.next_due_at <= p_now AND c.status <> 'disconnected' AND b.archived_at IS NULL
    ORDER BY c.next_due_at, c.id
    LIMIT p_limit
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION connections_by_external(p_kind text, p_external_id text)
RETURNS TABLE (id uuid, workspace_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = tuorps, pg_temp
AS $$
    SELECT c.id, c.workspace_id
    FROM connections c
    WHERE c.kind = p_kind AND c.external_id = p_external_id AND c.status <> 'disconnected'
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION grants_by_provider_user(p_provider text, p_provider_user_id text)
RETURNS TABLE (id uuid, workspace_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = tuorps, pg_temp
AS $$
    SELECT g.id, g.workspace_id
    FROM oauth_grants g
    WHERE g.provider = p_provider AND g.provider_user_id = p_provider_user_id
$$;
-- +goose StatementEnd

-- Grants created before p_before that still hold tokens but no live
-- connection: abandoned or fully disconnected logins, whose tokens are
-- wiped (ADR 0009).
-- +goose StatementBegin
CREATE FUNCTION grants_unused(p_before timestamptz)
RETURNS TABLE (id uuid, workspace_id uuid, brand_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = tuorps, pg_temp
AS $$
    SELECT g.id, g.workspace_id, g.brand_id
    FROM oauth_grants g
    WHERE g.created_at < p_before
      AND (g.access_token_enc IS NOT NULL OR g.refresh_token_enc IS NOT NULL)
      AND NOT EXISTS (SELECT 1 FROM connections c
                      WHERE c.workspace_id = g.workspace_id AND c.grant_id = g.id AND c.status <> 'disconnected')
    LIMIT 500
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION webhook_receipts_prune(p_before timestamptz)
RETURNS bigint
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = tuorps, pg_temp
AS $$
    WITH pruned AS (DELETE FROM webhook_receipts WHERE received_at < p_before RETURNING 1)
    SELECT count(*) FROM pruned
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION data_deletion_status(p_code text)
RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = tuorps, pg_temp
AS $$
    SELECT d.status FROM data_deletion_requests d WHERE d.confirmation_code = p_code
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION data_deletion_complete(p_code text, p_now timestamptz)
RETURNS boolean
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = tuorps, pg_temp
AS $$
    WITH done AS (
        UPDATE data_deletion_requests SET status = 'done', completed_at = p_now
        WHERE confirmation_code = p_code AND status = 'pending'
        RETURNING 1
    )
    SELECT EXISTS (SELECT 1 FROM done)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION connections_due(timestamptz, int), connections_by_external(text, text),
    grants_by_provider_user(text, text), grants_unused(timestamptz), webhook_receipts_prune(timestamptz),
    data_deletion_status(text), data_deletion_complete(text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION connections_due(timestamptz, int), connections_by_external(text, text),
    grants_by_provider_user(text, text), grants_unused(timestamptz), webhook_receipts_prune(timestamptz),
    data_deletion_status(text), data_deletion_complete(text, timestamptz) TO tuorps_app;

-- +goose Down
DROP FUNCTION data_deletion_complete(text, timestamptz);
DROP FUNCTION data_deletion_status(text);
DROP FUNCTION webhook_receipts_prune(timestamptz);
DROP FUNCTION grants_unused(timestamptz);
DROP FUNCTION grants_by_provider_user(text, text);
DROP FUNCTION connections_by_external(text, text);
DROP FUNCTION connections_due(timestamptz, int);
DROP TABLE data_deletion_requests;
DROP TABLE webhook_receipts;
DROP TABLE connections;
DROP TABLE oauth_grants;
