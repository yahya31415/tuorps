-- All queries run under row-level security as tuorps_app with the tenant
-- GUCs set, and also filter on workspace_id explicitly (defence in depth).

-- name: InsertGrant :exec
INSERT INTO oauth_grants (id, workspace_id, brand_id, provider, provider_user_id, scopes, missing_scopes,
    access_token_enc, refresh_token_enc, access_expires_at, refresh_expires_at, data_access_expires_at,
    token_issued_at, created_by, created_at, updated_at)
VALUES (@id, @workspace_id, @brand_id, @provider, @provider_user_id, @scopes, @missing_scopes,
    sqlc.narg('access_token_enc'), sqlc.narg('refresh_token_enc'), sqlc.narg('access_expires_at'),
    sqlc.narg('refresh_expires_at'), sqlc.narg('data_access_expires_at'), sqlc.narg('token_issued_at'),
    @created_by, @now, @now);

-- name: GetGrant :one
SELECT id, brand_id, provider, provider_user_id, scopes, missing_scopes, access_token_enc, refresh_token_enc,
    access_expires_at, refresh_expires_at, data_access_expires_at, token_issued_at, created_by, created_at
FROM oauth_grants WHERE workspace_id = @workspace_id AND brand_id = @brand_id AND id = @id;

-- name: WipeGrantTokens :execrows
UPDATE oauth_grants SET access_token_enc = NULL, refresh_token_enc = NULL, updated_at = @now
WHERE workspace_id = @workspace_id AND brand_id = @brand_id AND id = @id;

-- name: InsertConnection :exec
INSERT INTO connections (id, workspace_id, brand_id, grant_id, kind, external_id, display_name, token_enc,
    status, token_expires_at, next_due_at, created_at, updated_at)
VALUES (@id, @workspace_id, @brand_id, @grant_id, @kind, @external_id, @display_name, sqlc.narg('token_enc'),
    'active', sqlc.narg('token_expires_at'), sqlc.narg('next_due_at'), @now, @now);

-- name: GetConnection :one
SELECT c.id, c.brand_id, c.grant_id, g.provider, c.kind, c.external_id, c.display_name, c.token_enc, c.token_key_id, c.status,
    c.token_expires_at, c.last_checked_at, c.last_error, c.warned_days, c.next_due_at, c.created_at
FROM connections c JOIN oauth_grants g ON g.workspace_id = c.workspace_id AND g.brand_id = c.brand_id AND g.id = c.grant_id
WHERE c.workspace_id = @workspace_id AND c.brand_id = @brand_id AND c.id = @id;

-- name: ListConnections :many
-- Keyset by id. Index: connections_brand (workspace_id, brand_id, id).
SELECT c.id, c.brand_id, c.grant_id, g.provider, c.kind, c.external_id, c.display_name, c.token_enc, c.token_key_id, c.status,
    c.token_expires_at, c.last_checked_at, c.last_error, c.warned_days, c.next_due_at, c.created_at
FROM connections c JOIN oauth_grants g ON g.workspace_id = c.workspace_id AND g.brand_id = c.brand_id AND g.id = c.grant_id
WHERE c.workspace_id = @workspace_id AND c.brand_id = @brand_id AND c.id > @after
ORDER BY c.id LIMIT @page_size;

-- name: DisconnectConnection :execrows
UPDATE connections SET status = 'disconnected', token_enc = NULL, next_due_at = NULL, updated_at = @now
WHERE workspace_id = @workspace_id AND brand_id = @brand_id AND id = @id AND status <> 'disconnected';

-- name: RepointConnection :execrows
UPDATE connections SET grant_id = @grant_id, token_enc = sqlc.narg('token_enc'), token_expires_at = sqlc.narg('token_expires_at'),
    status = 'active', last_error = NULL, warned_days = 0, last_checked_at = @now, next_due_at = sqlc.narg('next_due_at'), updated_at = @now
WHERE workspace_id = @workspace_id AND brand_id = @brand_id AND id = @id AND status <> 'disconnected';

-- name: LiveConnectionsOnGrant :one
SELECT count(*) FROM connections
WHERE workspace_id = @workspace_id AND brand_id = @brand_id AND grant_id = @grant_id AND status <> 'disconnected';
