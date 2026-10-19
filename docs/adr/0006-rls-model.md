# ADR 0006: Row-level security model

- Status: accepted
- Date: 2026-10-19

## Context

Tuorps is multi-tenant: workspaces contain brands, and each user holds
per-brand roles. CLAUDE.md requires row-level security *and* a tenant check
in every repository call. Some lookups happen before we know the tenant or
the user: login by email, redeeming an emailed token, and looking up an
invitation by token.

## Decision

- **Two GUCs per transaction.** `postgres.InTx`, `InReadTx` and
  `audit.Writer.InWriteTx` run `set_config('app.workspace_id', …, true)` and
  `set_config('app.user_id', …, true)` from a `postgres.Scope`.
  `current_workspace_id()` and `current_user_id()` return NULL when a GUC is
  unset or empty, and NULL matches no row, so **tenancy fails closed**.
- **Policies**, for role `tuorps_app` (`NOBYPASSRLS`):
  - Tenant tables (brands, brand_roles, invitations) match
    `workspace_id = current_workspace_id()`.
  - `workspaces` and `workspace_members` are also readable for the caller's
    own memberships, which is how "my workspaces" works.
  - `users`: self, plus co-members of the current workspace (read only).
  - `user_credentials`, `recovery_codes`, `email_tokens`: self only.
  - `audit_log`: append and read within the workspace, or account-level rows
    for the actor. `UPDATE`, `DELETE` and `TRUNCATE` are revoked, so the log
    is append-only.
- **No `FORCE ROW LEVEL SECURITY`.** The owner role `tuorps_migrator` runs
  migrations and owns the definer functions. The application never connects
  as it. RLS binds `tuorps_app`, which is the role the app uses.
- **Credentials live in `user_credentials`, apart from `users`.** Co-members
  can read each other's profiles (name, email) but never a password hash or
  TOTP secret.
- **Pre-authentication lookups** go through three narrow
  `SECURITY DEFINER` functions, owned by the migrator, with
  `SET search_path = tuorps, pg_temp`. Each returns the minimum columns:
  - `auth_lookup_login(email)` returns the id, the hash and the
    verified/disabled/MFA flags for login.
  - `auth_redeem_email_token(hash, purpose, now)` consumes a verify or reset
    token atomically and returns its user.
  - `invitation_by_token(hash, now)` returns a pending invitation.

  Each takes a SHA-256 token hash (never the token) and grants nothing
  broader.
- **Composite foreign keys** on `(workspace_id, …)`, for example
  `brand_roles(workspace_id, brand_id) → brands(workspace_id, id)`, make
  cross-tenant references impossible regardless of RLS.
- **Defence in depth.** Repository functions take an explicit scope and
  filter `WHERE workspace_id = $1` as well. Handlers report a workspace or
  brand the caller cannot see as 404, never 403.

## Consequences

- Every query must run inside a scoped transaction. A forgotten scope
  returns nothing; it does not leak.
- RLS tests run as `tuorps_app` against real Postgres:
  - cross-tenant invisibility per table;
  - fail-closed behaviour with no GUC;
  - the append-only audit log.
- Policy subqueries on `workspace_members` are indexed (`(user_id,
  workspace_id)` and the primary key). Each keyset list query names its
  index in its sqlc query comment.
