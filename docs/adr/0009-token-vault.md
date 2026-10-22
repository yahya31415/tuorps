# ADR 0009: Token vault for platform connections

- Status: accepted
- Date: 2026-10-22

## Context

M3 connects a brand's Facebook Pages, Instagram accounts, LinkedIn pages and
YouTube channels. Every connection holds platform tokens that can publish
and read private messages on the customer's behalf, so CLAUDE.md requires
them to be encrypted at rest. Token behaviour differs per platform
(`docs/api-reference.md`, "OAuth, tokens and webhook security"):

| Platform | Stored token | Lifetime | Renewal |
| --- | --- | --- | --- |
| Facebook Page | Page token from a long-lived user token | No expiry while the grant stays valid | None; health check only |
| Instagram via Facebook Login | The linked Page's token | As above | As above |
| Instagram Login | Long-lived IG user token | 60 days | Refresh after 24 h, before expiry |
| LinkedIn | Member access token | 60 days | Refresh token only for approved partners; otherwise re-consent |
| YouTube | Google refresh + access token | Access ~1 h; refresh until revoked or unused 6 months | Refresh on demand |

WhatsApp system-user tokens (M6) do not expire.

## Decision

- **Two tables, both brand-scoped and under RLS** (ADR 0006):
  - `oauth_grants`: one row per completed OAuth flow. It holds the user-level
    tokens (Meta user token, IG user token, LinkedIn token, Google refresh
    token), the provider user ID, the granted scopes and the expiries.
  - `connections`: one row per account attached to a brand. It points to its
    grant and may hold its own token (a Facebook Page token).
  - A composite FK `(workspace_id, brand_id, grant_id) → oauth_grants
    (workspace_id, brand_id, id)` keeps a connection on its grant's brand.
    A grant is never shared across brands: connecting the same Meta user to
    two brands runs two OAuth flows, so one brand's admins can never use
    another brand's grant.
- **Encryption**: AES-256-GCM through the keyring, stored as text in the
  keyring encoding (`v1.<kid>.<b64>`), as with TOTP secrets. A generated
  `*_key_id` column per token, with a partial index, finds rows sealed with a
  given key.
- **AAD binds each ciphertext to its row and field**:
  `connections/<grant-or-connection-id>/<access_token|refresh_token|page_token>`.
  Ciphertexts cannot be swapped between rows or between token kinds.
- **Re-seal on every maintenance pass.** The maintenance job opens each token
  it touches; when the token's key ID is not the active one, it re-seals it.
  The sweep visits every connection at least daily, so an old key can be
  retired once `*_key_id` shows no rows still use it.
- **Refresh strategy**:
  - Google: refresh on demand in `vault.AccessToken` when 5 minutes or less
    remain. M4 and M5 call it. The daily check also refreshes, which proves
    the refresh token is still valid.
  - Instagram Login: the maintenance job refreshes once the token is older
    than 24 h and before it expires.
  - LinkedIn: store and use a refresh token if one is returned. Otherwise
    email brand admins 14 and 3 days before expiry (once each, tracked in
    `warned_threshold`), then mark the connection `needs_reauth`.
  - Meta Page tokens: a daily `debug_token` health check. Error 190 (or
    `is_valid=false`) marks `needs_reauth`. The check also tracks the
    grant's `data_access_expires_at` (90 days after the user was last
    active; `instagram_*` permissions are not exempt) and warns before it
    lapses.
- **Lifetimes always come from the response** (`expires_in`). The docs'
  samples contradict their prose (Google 3920 s; LinkedIn 86400 s), so
  nothing is hard-coded.
- **Jobs run with a workspace scope and no user**:
  `postgres.Scope{WorkspaceID: ws}`. Cross-tenant discovery (the sweep,
  webhook routing, data deletion) goes through narrow `SECURITY DEFINER`
  functions that return only `(id, workspace_id)`. Audit rows from jobs have
  a NULL actor, which `audit_log_append` allows.
- **Errors never carry token material.** Adapters rebuild errors as
  `provider/operation/status/code` and drop `*url.Error` (its text includes
  the URL, and Instagram's long-lived exchange puts the app secret in the
  query string). `connections.last_error` stores only the fixed code.
- **Wiping**: disconnect, data deletion and abandoned grants (no connection
  after 1 h) set the token columns to NULL. The rows stay for audit.

## Consequences

- Publishing (M4) and the inbox (M5) get tokens only through the vault.
- Losing a keyring key makes the tokens sealed with it unreadable. The
  affected connections fall to `needs_reauth` and the customer reconnects;
  there is no other data loss.
- LinkedIn customers reconnect every 60 days until Tuorps is an approved
  partner with refresh tokens.
- Google can push token-revoked events (RISC). They are not used in M3; the
  daily check catches revocation within a day. Revisit for faster detection.
