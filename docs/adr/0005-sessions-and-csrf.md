# ADR 0005: Cookie sessions in Redis, CSRF via Cross-Origin Protection

- Status: accepted
- Date: 2026-10-19

## Context

The first client is a browser app on the same site as the API. The options
were:

- bearer tokens in JavaScript-readable storage, which XSS can exfiltrate;
- signed or encrypted stateless cookies, which are hard to revoke;
- opaque server-side sessions.

ASVS L2 requires:

- sessions that can be revoked (logout everywhere, and on password or 2FA
  changes);
- idle and absolute timeouts;
- rotation at privilege changes;
- CSRF defence.

## Decision

- **Opaque session token** of 32 random bytes in cookie
  `__Host-tuorps_session`: `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`, no
  `Domain`.
- **Stored in Redis** under `tuorps:v1:sess:<sha256(token)>`, so a Redis dump
  never yields usable tokens. The record holds the user, the MFA state and
  the created and last-seen times.
  - The idle timeout (`SESSION_IDLE_TIMEOUT`, 12h) is enforced by TTL and
    refreshed on use.
  - The absolute timeout (`SESSION_ABSOLUTE_TIMEOUT`, 14d) is checked
    against the creation time.
  - A per-user set `tuorps:v1:usess:<user>` lets us revoke all of a user's
    sessions.
- **Rotation**: a new token at login, and again after the second factor.
  Password change, password reset and 2FA changes revoke the user's other
  sessions.
- **The cookie is always `Secure`.** The `__Host-` prefix requires it.
  `COOKIE_SECURE` was removed and setting it is now a startup error.
  Browsers treat `http://localhost` as a secure context, so local
  development still works. Plain-HTTP origins other than localhost are
  unsupported by design. In production `APP_ORIGIN` must be `https`.
- **CSRF**: stdlib `http.CrossOriginProtection` (Go 1.25+).
  - Unsafe methods carrying `Sec-Fetch-Site: cross-site`, or a mismatched
    `Origin`, get 403 `cross_origin_request`.
  - `APP_ORIGIN` is the one trusted origin.
  - `SameSite=Lax` is a second layer.
  - No token plumbing is needed in the client.
- MFA login uses a separate 5-minute challenge, stored in Redis and
  single-use, so the password step never yields a session on its own.

## Consequences

- Redis is on the authentication path. Auth rate limits fail closed (503)
  when Redis is down. Other writes fail open, with an error logged.
- The API is cookie-first. Native mobile or third-party API clients will
  need a bearer-token scheme later (its own ADR), and must not weaken
  this one.
