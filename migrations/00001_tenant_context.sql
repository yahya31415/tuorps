-- +goose Up
-- Tenant context for row-level security (M2 adds the policies).
-- Each request transaction runs: SELECT set_config('app.workspace_id', $1, true)
-- and RLS policies compare rows against current_workspace_id().
-- Unset or empty yields NULL, which matches no row: tenancy fails closed.
CREATE FUNCTION current_workspace_id() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$ SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid $$;

COMMENT ON FUNCTION current_workspace_id() IS
    'Workspace of the current transaction (app.workspace_id GUC); NULL when unset.';

-- +goose Down
DROP FUNCTION current_workspace_id();
