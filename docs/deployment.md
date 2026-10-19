# Deployment

## Data residency (India)

Customer data for Indian customers is stored and processed **only in India
regions**, which supports the DPDP Act and the "India data residency" MVP
feature.

| Component | Production placement |
| --- | --- |
| Compute (api, worker) | AWS `ap-south-1` (Mumbai) primary; `ap-south-2` (Hyderabad) for DR. GCP `asia-south1` / `asia-south2` are equivalent. |
| PostgreSQL 18 | Managed Postgres in the same region; synchronous standby in a second AZ; cross-region replica in the DR region only. |
| Redis 8 | Managed Redis in the same region (sessions, rate limits, caches: non-authoritative). |
| Object storage (media) | S3-compatible bucket in the same region; replication only to the India DR region. |
| Backups and snapshots | Encrypted, stored in India regions only; retention per DPDP policy. |
| Logs | Shipped to a log store in an India region. Logs are redacted at source (`internal/platform/logx`). |

Rules:
- No service, backup or log sink outside India receives customer data.
- Third-party processors (LLM vendors, email or WhatsApp providers) must be
  reviewed for residency before use.
- Region choice is infrastructure configuration, not code. The application
  takes its endpoints from `DATABASE_URL`, `REDIS_URL` and (from M4) the S3
  endpoint variables.

## Single-box Compose deployment

`compose.yaml` is production-safe on its own (do not add `compose.dev.yaml`):

- Only the API is published, on `127.0.0.1:8080`. Terminate TLS in a reverse
  proxy on the host and set `TUORPS_ENV=prod` (enables HSTS).
- Postgres, Redis and Garage sit on an `internal` network with no route to
  the internet. The api and worker also join `egress` to reach platform APIs.
- Every container has a read-only root filesystem, `cap_drop: [ALL]`,
  `no-new-privileges`, a tmpfs `/tmp` and a healthcheck. App images are
  distroless static, non-root (`nonroot:nonroot`), with no shell.
- Startup order: postgres/redis healthy → `migrate` runs `api migrate up` as
  `tuorps_migrator` and exits 0 → api and worker start.

### Secrets

All secrets are Docker secrets under `/run/secrets`, read through `*_FILE`
variables. They are never placed in plain environment variables or images.

| Secret | Used by |
| --- | --- |
| `postgres_password`, `pg_app_password`, `pg_migrator_password` | postgres init (`deploy/postgres/initdb/10-roles.sh`) |
| `database_url` | api, worker (`tuorps_app`: no DDL, `NOBYPASSRLS`) |
| `migrate_database_url` | migrate (`tuorps_migrator`: owns schema `tuorps`) |
| `redis_url`, `redis_acl` | api, worker / redis (default user disabled; app user has no `@dangerous` commands; `healthcheck` user may only `PING`) |
| `garage_rpc_secret`, `garage_admin_token`, `garage_access_key_id`, `garage_secret_key` | garage / storage bootstrap |
| `encryption_keys` | api, worker: AES-256-GCM keyring (`kid:base64` lines) for TOTP secrets and queued-email payloads |
| `smtp_password` | worker, only when `SMTP_PASSWORD_FILE=/run/secrets/smtp_password` is set |

For local development, `make dev-secrets` generates them into `secrets/`
(gitignored, directory mode 0700). In production, provision them from the
cloud secret manager, and use `sslmode=verify-full` in the database URLs.

### Commands

```sh
make dev-secrets   # once
make up            # build, start, wait for healthy (adds compose.dev.yaml)
make storage-init  # once: Garage layout, key, bucket (idempotent)
make migrate-status
make down
```

## Configuration reference

Every variable is validated at startup. The process exits listing **every**
missing or invalid variable, and never echoes values.

| Variable | Default | Notes |
| --- | --- | --- |
| `DATABASE_URL` / `_FILE` | required | `postgres://` or `postgresql://` |
| `REDIS_URL` / `_FILE` | required | `redis://` or `rediss://` |
| `MIGRATE_DATABASE_URL` / `_FILE` | required for `migrate` | schema-owner role |
| `TUORPS_ENV` | `dev` | `dev` or `prod` |
| `HTTP_ADDR` / `WORKER_HEALTH_ADDR` | `:8080` / `:8081` | |
| `LOG_LEVEL` | `info` | debug, info, warn, error |
| `SHUTDOWN_TIMEOUT` / `READINESS_TIMEOUT` | `15s` / `2s` | |
| `HTTP_READ_HEADER_TIMEOUT`, `HTTP_READ_TIMEOUT`, `HTTP_WRITE_TIMEOUT`, `HTTP_IDLE_TIMEOUT` | `5s`, `15s`, `30s`, `120s` | |
| `HTTP_MAX_BODY_BYTES` | `1MiB` | integer or KiB/MiB/GiB |
| `DB_MAX_CONNS` / `DB_MIN_CONNS` | `20` / `2` | min ≤ max |
| `DB_MAX_CONN_LIFETIME` / `DB_MAX_CONN_IDLE_TIME` | `1h` / `15m` | |
| `REDIS_POOL_SIZE` | `20` | |
| `REDIS_DIAL_TIMEOUT` / `REDIS_READ_TIMEOUT` / `REDIS_WRITE_TIMEOUT` | `2s` / `1s` / `1s` | |
| `APP_ORIGIN` | `http://localhost:8080` | The browser app's origin: email links and the trusted CSRF origin. **Required and `https` when `TUORPS_ENV=prod`.** |
| `SESSION_IDLE_TIMEOUT` / `SESSION_ABSOLUTE_TIMEOUT` | `12h` / `336h` (14d) | idle ≤ absolute |
| `TRUSTED_PROXY_CIDRS` | none | Comma-separated CIDRs of your reverse proxies. `X-Forwarded-For` is honoured only from these (client IP for rate limits and audit). |
| `ARGON2_MAX_CONCURRENCY` | `4` | Concurrent password hashes (each uses 46 MiB). |
| `ENCRYPTION_KEYS` / `_FILE` | required | One `kid:base64(32 bytes)` per line. Keep old keys to read old data. |
| `ENCRYPTION_ACTIVE_KEY_ID` | required | Key ID used for new encryptions; must be listed in `ENCRYPTION_KEYS`. |
| `SMTP_HOST` | required by the worker | |
| `SMTP_PORT` | `587` | |
| `SMTP_TLS` | `starttls` | `starttls`, `implicit` (465) or `none` (only with `TUORPS_ENV=dev`) |
| `SMTP_USERNAME` / `SMTP_PASSWORD` / `_FILE` | none | A password requires a username. |
| `SMTP_TIMEOUT` | `10s` | Dial and I/O deadline per send. |
| `MAIL_FROM` | required by the worker | e.g. `Tuorps <no-reply@mail.example.in>` |

`COOKIE_SECURE` has been removed: the session cookie is always `Secure`
(ADR 0005). Setting it is a startup error, so stale configuration cannot
silently weaken the cookie.

Every database session also gets `statement_timeout=15s` and
`idle_in_transaction_session_timeout=30s`.

## Email delivery

The worker sends transactional email (verification, password reset,
invitations, security notices) over SMTP (ADR 0007). The API only queues
messages, inside the same transaction as the write that caused them
(ADR 0004).

Recommended for India residency: **Amazon SES in `ap-south-1` (Mumbai)**.

1. Verify the sending domain in SES `ap-south-1`. Publish its DKIM CNAMEs,
   SPF (`include:amazonses.com`) and a DMARC policy.
2. Create SMTP credentials. These are SES SMTP credentials, not an IAM
   access key.
3. Configure the worker:

   ```sh
   SMTP_HOST=email-smtp.ap-south-1.amazonaws.com
   SMTP_PORT=587 SMTP_TLS=starttls
   SMTP_USERNAME=<SES SMTP username>
   SMTP_PASSWORD_FILE=/run/secrets/smtp_password   # secrets/smtp_password holds the password
   MAIL_FROM="Tuorps <no-reply@mail.example.in>"
   ```

ZeptoMail (Zoho, India data centre) and Postmark work the same way. Locally,
`make up` routes all mail to Mailpit; open <http://127.0.0.1:8025>.

## Session and proxy setup

- Serve the browser app and the API from the **same site** over HTTPS, and
  set `APP_ORIGIN` to the app's origin. Cross-origin writes are refused.
- Put the reverse proxy's addresses in `TRUSTED_PROXY_CIDRS`, so that rate
  limits and audit entries see the real client IP. Otherwise every client
  appears as the proxy.
- The audit log stores a keyed hash of the client IP (HMAC derived from the
  keyring), never the raw IP.

