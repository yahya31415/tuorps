# ADR 0008: OAuth connection flow on the standard library

- Status: accepted
- Date: 2026-10-22

## Context

M3 lets brand admins connect Facebook Pages, Instagram accounts (through
Facebook Login and through Instagram Login), LinkedIn pages and YouTube
channels. Each uses the OAuth 2.0 authorization-code flow with a confidential
client (`docs/api-reference.md`, "OAuth, tokens and webhook security").

Points from the reference:
- **No platform documents PKCE for a server-side web client.** Meta covers
  it only in an OIDC variant that does not mention `config_id`; LinkedIn and
  Google cover only native or installed apps; Instagram says nothing.
- `state` is optional on Instagram and LinkedIn, but all four echo it back.
- Meta's code exchange and Instagram's long-lived exchange are documented
  only as GETs with the app secret in the query string.

`golang.org/x/oauth2` is not a dependency. ADR 0007 refers to an "allowed
dependency list", which is not in this repo. The flows are four HTTP calls
each, so the standard library is enough.

## Decision

- **Standard library only.** A pure `connections/core` builds the authorize
  URLs and parses token responses. Adapters send the requests over a
  provider client: `httpx.NewClient` timeouts, **no redirects**, response
  bodies capped with `io.LimitReader`, base URL fixed in production and
  injectable in tests only. A `paging.next` URL is followed only when its
  host matches the base URL.
- **No PKCE in M3.** It is added per provider when the provider documents it
  for web clients. The defence against login CSRF and code injection is
  `state`:
  - 32 random bytes, sent to the platform base64url-encoded.
  - Stored in Redis under `tuorps:v1:oauth:<sha256(state)>` with a
    **10-minute TTL**, and consumed with **`GETDEL`** (single use, atomic).
  - The stored value is sealed with the keyring (AAD `oauth/state`). It
    holds the user, the SHA-256 of the session token, the workspace, the
    brand, the provider, and the connection ID when reconnecting.
- **The callback** is `GET /v1/oauth/{provider}/callback`. The platform
  redirects the browser there, so it is a top-level navigation, the
  `SameSite=Lax` session cookie is sent, and cross-origin protection does
  not apply to GET. It succeeds only if all of these hold:
  - the session is valid;
  - the state exists and is consumed now;
  - the state's provider equals `{provider}` (no mix-ups);
  - the state's user and session hash equal the current session's;
  - `PermConnectionManage` still holds on the state's brand (roles can
    change during the TTL), and the brand is not archived.

  Any failure redirects (303) to a fixed `APP_ORIGIN/connections/error#reason=<code>`.
  `<code>` comes from a closed set. The platform's `error_description` is
  never echoed.

  On success it exchanges the code, stores the grant (ADR 0009), audits
  `connection.grant`, and redirects to
  `APP_ORIGIN/connections/select#grant=<id>`. Codes, state and tokens never
  appear in a URL Tuorps emits; the access log records paths only.
- **Account selection.** `GET …/grants/{grant}/accounts` lists the
  candidates live from the platform, and only for the user who created the
  grant. `POST …/connections` fetches the candidates again and rejects any
  `external_id` that is not among them.
- **Redirect URIs** are `APP_ORIGIN + /v1/oauth/{provider}/callback`, one per
  provider. They are registered exactly (LinkedIn and Google require an
  exact match).
- **Secrets in query strings** (Meta, Instagram long-lived exchange): the
  adapters rebuild errors as `provider/op/status/code` and drop
  `*url.Error`, so URLs never reach logs or `river_job.errors`. Meta calls
  also carry `appsecret_proof`.
- **Meta token type**: user access tokens through a Facebook Login for
  Business configuration (`config_id`). Business integration system user
  tokens (never expiring) are left for M6, where WhatsApp Embedded Signup
  needs them anyway.
- **Granted scopes** are compared with the requested ones (Google lets users
  grant a subset; Meta reports `granular_scopes`). A missing scope is
  recorded on the grant and shown to the user, not treated as a failure.

## Consequences

- No OAuth dependency to track or audit.
- New providers are added with a URL builder, a response parser and an
  adapter. The flow and the state store are shared.
- When a provider documents PKCE for web clients, adding it is a contained
  change: the verifier goes in the sealed state, and the challenge goes in
  the URL builder.
