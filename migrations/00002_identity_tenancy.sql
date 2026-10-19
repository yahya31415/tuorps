-- +goose Up
-- Identity, tenancy and audit (M2). See docs/adr/0006-rls-model.md.
--
-- Row-level security applies to tuorps_app (not the owner, NOBYPASSRLS).
-- Every request transaction sets two GUCs:
--   app.workspace_id -> current_workspace_id()   (tenant boundary)
--   app.user_id      -> current_user_id()        (caller identity)
-- Both read as NULL when unset, which matches no row: tenancy fails closed.
-- Pre-authentication lookups use the narrow SECURITY DEFINER functions at
-- the end of this file, which run as the owner.
--
-- Composite foreign keys on (workspace_id, ...) make cross-tenant references
-- impossible regardless of RLS.

CREATE FUNCTION current_user_id() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$ SELECT NULLIF(current_setting('app.user_id', true), '')::uuid $$;

-- Profiles: visible to the user and to co-members of the current workspace.
CREATE TABLE users (
    id                uuid PRIMARY KEY,
    email             text NOT NULL CHECK (length(email) BETWEEN 3 AND 254),
    email_normalized  text NOT NULL UNIQUE CHECK (email_normalized = lower(email_normalized)),
    email_verified_at timestamptz,
    display_name      text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 100),
    locale            text NOT NULL DEFAULT 'en' CHECK (length(locale) BETWEEN 2 AND 16),
    disabled_at       timestamptz,
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL
);

-- Credentials: visible only to their owner, never to co-members.
-- totp_secret_enc is a keyring encoding "v1.<key-id>.<data>"; totp_key_id
-- exposes the key ID so rotation can find records sealed with old keys.
CREATE TABLE user_credentials (
    user_id         uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    password_hash   text NOT NULL,
    totp_secret_enc text,
    totp_key_id     text GENERATED ALWAYS AS (split_part(totp_secret_enc, '.', 2)) STORED,
    totp_enabled_at timestamptz,
    totp_last_step  bigint,
    updated_at      timestamptz NOT NULL,
    CHECK (totp_enabled_at IS NULL OR totp_secret_enc IS NOT NULL)
);
CREATE INDEX user_credentials_totp_key_id ON user_credentials (totp_key_id) WHERE totp_key_id IS NOT NULL;

CREATE TABLE recovery_codes (
    user_id   uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash bytea NOT NULL CHECK (length(code_hash) = 32),
    used_at   timestamptz,
    PRIMARY KEY (user_id, code_hash)
);

-- Single-use, expiring tokens sent by email; only SHA-256 hashes are stored.
CREATE TABLE email_tokens (
    id         uuid PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose    text NOT NULL CHECK (purpose IN ('verify_email', 'reset_password')),
    token_hash bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL
);
CREATE INDEX email_tokens_user ON email_tokens (user_id, purpose);

CREATE TABLE workspaces (
    id         uuid PRIMARY KEY,
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    created_by uuid NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

-- Membership; the workspace owner holds every permission (billing is per brand).
CREATE TABLE workspace_members (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    is_owner     boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, user_id)
);
-- "My workspaces" keyset list: WHERE user_id = $1 AND workspace_id > $2 ORDER BY workspace_id.
-- EXPLAIN: Index Only Scan using workspace_members_user on workspace_members.
CREATE INDEX workspace_members_user ON workspace_members (user_id, workspace_id);

CREATE TABLE brands (
    id           uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    archived_at  timestamptz,
    -- Target of composite FKs; also serves the keyset list
    -- WHERE workspace_id = $1 AND id > $2 ORDER BY id (EXPLAIN: Index Only Scan using brands_workspace_id).
    CONSTRAINT brands_workspace_id UNIQUE (workspace_id, id)
);
CREATE UNIQUE INDEX brands_active_name ON brands (workspace_id, lower(name)) WHERE archived_at IS NULL;

CREATE TABLE brand_roles (
    workspace_id uuid NOT NULL,
    brand_id     uuid NOT NULL,
    user_id      uuid NOT NULL,
    role         text NOT NULL CHECK (role IN ('admin', 'publisher', 'agent', 'viewer')),
    created_at   timestamptz NOT NULL,
    PRIMARY KEY (brand_id, user_id),
    FOREIGN KEY (workspace_id, brand_id) REFERENCES brands (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, user_id) REFERENCES workspace_members (workspace_id, user_id) ON DELETE CASCADE
);
CREATE INDEX brand_roles_member ON brand_roles (workspace_id, user_id);

CREATE TABLE invitations (
    id               uuid PRIMARY KEY,
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    email_normalized text NOT NULL CHECK (email_normalized = lower(email_normalized)),
    brand_roles      jsonb NOT NULL CHECK (jsonb_typeof(brand_roles) = 'array'),
    invited_by       uuid NOT NULL REFERENCES users (id),
    token_hash       bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    expires_at       timestamptz NOT NULL,
    accepted_at      timestamptz,
    accepted_by      uuid REFERENCES users (id),
    revoked_at       timestamptz,
    created_at       timestamptz NOT NULL
);
-- Keyset list WHERE workspace_id = $1 AND id < $2 ORDER BY id DESC
-- (EXPLAIN: Index Only Scan Backward using invitations_workspace).
CREATE INDEX invitations_workspace ON invitations (workspace_id, id);

-- Activity log. Workspace events carry workspace_id; account-level security
-- events (password change, 2FA) carry NULL and are visible to their actor.
-- No foreign keys: the record outlives what it describes.
CREATE TABLE audit_log (
    id            uuid PRIMARY KEY,
    workspace_id  uuid,
    actor_user_id uuid,
    action        text NOT NULL CHECK (length(action) BETWEEN 1 AND 64),
    target_type   text NOT NULL CHECK (length(target_type) BETWEEN 1 AND 32),
    target_id     text NOT NULL CHECK (length(target_id) <= 64),
    request_id    text NOT NULL DEFAULT '' CHECK (length(request_id) <= 64),
    ip_hash       text NOT NULL DEFAULT '' CHECK (length(ip_hash) <= 64),
    details       jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(details) = 'object'),
    at            timestamptz NOT NULL
);
-- Keyset list WHERE workspace_id = $1 AND id < $2 ORDER BY id DESC
-- (EXPLAIN: Index Only Scan Backward using audit_log_workspace).
CREATE INDEX audit_log_workspace ON audit_log (workspace_id, id) WHERE workspace_id IS NOT NULL;
-- Own security events WHERE workspace_id IS NULL AND actor_user_id = $1 AND id < $2 ORDER BY id DESC.
CREATE INDEX audit_log_account ON audit_log (actor_user_id, id) WHERE workspace_id IS NULL;
REVOKE UPDATE, DELETE, TRUNCATE ON audit_log FROM tuorps_app;

-- Row-level security -------------------------------------------------------

ALTER TABLE users ENABLE ROW LEVEL SECURITY;
CREATE POLICY users_self ON users
    USING (id = current_user_id()) WITH CHECK (id = current_user_id());
CREATE POLICY users_comembers ON users FOR SELECT
    USING (id IN (SELECT user_id FROM workspace_members WHERE workspace_id = current_workspace_id()));

ALTER TABLE user_credentials ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_credentials_self ON user_credentials
    USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());

ALTER TABLE recovery_codes ENABLE ROW LEVEL SECURITY;
CREATE POLICY recovery_codes_self ON recovery_codes
    USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());

ALTER TABLE email_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY email_tokens_self ON email_tokens
    USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());

ALTER TABLE workspaces ENABLE ROW LEVEL SECURITY;
CREATE POLICY workspaces_read ON workspaces FOR SELECT
    USING (id = current_workspace_id()
           OR id IN (SELECT workspace_id FROM workspace_members WHERE user_id = current_user_id()));
CREATE POLICY workspaces_create ON workspaces FOR INSERT
    WITH CHECK (created_by = current_user_id() AND id = current_workspace_id());
CREATE POLICY workspaces_update ON workspaces FOR UPDATE
    USING (id = current_workspace_id()) WITH CHECK (id = current_workspace_id());
CREATE POLICY workspaces_delete ON workspaces FOR DELETE
    USING (id = current_workspace_id());

ALTER TABLE workspace_members ENABLE ROW LEVEL SECURITY;
CREATE POLICY workspace_members_read ON workspace_members FOR SELECT
    USING (workspace_id = current_workspace_id() OR user_id = current_user_id());
CREATE POLICY workspace_members_write ON workspace_members FOR INSERT
    WITH CHECK (workspace_id = current_workspace_id());
CREATE POLICY workspace_members_update ON workspace_members FOR UPDATE
    USING (workspace_id = current_workspace_id()) WITH CHECK (workspace_id = current_workspace_id());
CREATE POLICY workspace_members_delete ON workspace_members FOR DELETE
    USING (workspace_id = current_workspace_id());

ALTER TABLE brands ENABLE ROW LEVEL SECURITY;
CREATE POLICY brands_tenant ON brands
    USING (workspace_id = current_workspace_id()) WITH CHECK (workspace_id = current_workspace_id());

ALTER TABLE brand_roles ENABLE ROW LEVEL SECURITY;
CREATE POLICY brand_roles_tenant ON brand_roles
    USING (workspace_id = current_workspace_id()) WITH CHECK (workspace_id = current_workspace_id());

ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
CREATE POLICY invitations_tenant ON invitations
    USING (workspace_id = current_workspace_id()) WITH CHECK (workspace_id = current_workspace_id());

ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY audit_log_read ON audit_log FOR SELECT
    USING (workspace_id = current_workspace_id()
           OR (workspace_id IS NULL AND actor_user_id = current_user_id()));
CREATE POLICY audit_log_append ON audit_log FOR INSERT
    WITH CHECK (workspace_id = current_workspace_id()
                OR (workspace_id IS NULL AND actor_user_id = current_user_id()));

-- Pre-authentication functions (SECURITY DEFINER, owner privileges) --------
-- Each answers one narrow question and returns the minimum columns.

-- +goose StatementBegin
CREATE FUNCTION auth_lookup_login(p_email_normalized text)
RETURNS TABLE (id uuid, email text, display_name text, locale text, password_hash text,
               email_verified boolean, totp_enabled boolean, disabled boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = tuorps, pg_temp
AS $$
    SELECT u.id, u.email, u.display_name, u.locale, c.password_hash,
           u.email_verified_at IS NOT NULL, c.totp_enabled_at IS NOT NULL, u.disabled_at IS NOT NULL
    FROM users u JOIN user_credentials c ON c.user_id = u.id
    WHERE u.email_normalized = p_email_normalized
$$;
-- +goose StatementEnd

-- Marks a token used and returns its user, atomically; NULL if the token is
-- unknown, for another purpose, expired or already used.
-- +goose StatementBegin
CREATE FUNCTION auth_redeem_email_token(p_token_hash bytea, p_purpose text, p_now timestamptz)
RETURNS uuid
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = tuorps, pg_temp
AS $$
    UPDATE email_tokens SET used_at = p_now
    WHERE token_hash = p_token_hash AND purpose = p_purpose AND used_at IS NULL AND expires_at > p_now
    RETURNING user_id
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION invitation_by_token(p_token_hash bytea, p_now timestamptz)
RETURNS TABLE (id uuid, workspace_id uuid, email_normalized text, brand_roles jsonb, expires_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = tuorps, pg_temp
AS $$
    SELECT i.id, i.workspace_id, i.email_normalized, i.brand_roles, i.expires_at
    FROM invitations i
    WHERE i.token_hash = p_token_hash AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > p_now
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION auth_lookup_login(text), auth_redeem_email_token(bytea, text, timestamptz),
    invitation_by_token(bytea, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_lookup_login(text), auth_redeem_email_token(bytea, text, timestamptz),
    invitation_by_token(bytea, timestamptz) TO tuorps_app;

-- +goose Down
-- These policies reference workspace_members, so they go before the tables.
DROP POLICY workspaces_read ON workspaces;
DROP POLICY users_comembers ON users;
DROP FUNCTION invitation_by_token(bytea, timestamptz);
DROP FUNCTION auth_redeem_email_token(bytea, text, timestamptz);
DROP FUNCTION auth_lookup_login(text);
DROP TABLE audit_log;
DROP TABLE invitations;
DROP TABLE brand_roles;
DROP TABLE brands;
DROP TABLE workspace_members;
DROP TABLE workspaces;
DROP TABLE email_tokens;
DROP TABLE recovery_codes;
DROP TABLE user_credentials;
DROP TABLE users;
DROP FUNCTION current_user_id();
