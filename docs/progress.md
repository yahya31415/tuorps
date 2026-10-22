# Progress

## Status

| Milestone | State |
| --- | --- |
| Step 0: CLAUDE.md + TDD guard | ✅ done |
| M1 Foundation | ✅ done (2026-10-19) |
| M2 Auth, tenancy, audit and email | ✅ done (2026-10-19) |
| M3 Account connections and token vault | next |
| M4 Publishing | — |
| M5 Unified inbox | — |
| M6 WhatsApp suite | — |
| M7 Analytics | — |
| M8 Approvals and agencies | — |
| M9 Billing and compliance | — |
| M10 Extras (email moved to M2) | — |

## Step 0: CLAUDE.md and the TDD guard

- `CLAUDE.md` contains the rules verbatim.
- `scripts/tdd-guard.sh` is a PreToolUse hook for Edit/Write/MultiEdit and
  Bash.
- `scripts/tdd.sh` implements `make red` / `make green`.
- `scripts/coverage-gate.sh` enforces `core/` ≥ 90% and overall ≥ 80%.
- All three are specified black-box by Go tests in `scripts/` (written red
  first), mutation-checked, and pass shellcheck.
- Verified live in this session: a Write to the guard's state directory is
  blocked with exit 2.

## M1: Foundation

Delivered:
- **Config** (`internal/platform/config`):
  - Reads env plus Docker secrets (`*_FILE`), with typed parsing and
    validation.
  - Reports every problem at once, never echoes values, and wraps secrets in
    a redacting `Secret` type.
  - A separate `LoadMigrate` covers the schema-owner URL.
- **Logging** (`internal/platform/logx`): a JSON slog handler that redacts
  sensitive keys and values (emails, +91/E.164 phones, bearer/JWT/Meta/Google
  tokens, URL passwords) in messages, groups, errors and LogValuers.
- **HTTP plumbing** (`internal/platform/httpx`):
  - Canonical error shape and request IDs (UUIDv7).
  - Strict decoder on `encoding/json/v2` with a size limit and a validation
    hook.
  - Recover, body-limit, security-header and access-log middleware.
  - Server/client constructors with every timeout set, and safe redirects.
  - Router answering 404/405 in JSON.
- **Health**: pure `core.Evaluate`, a concurrent deadline-bounded
  `RunChecks`, Postgres/Redis adapters, and `/healthz` + `/readyz`.
- **Infrastructure**:
  - pgx pool: runs as the `tuorps_app` role with session guards.
  - Redis client: timeouts, plus a startup ping.
  - Postgres role bootstrap: `tuorps_migrator` owns schema `tuorps`;
    `tuorps_app` is DML-only and `NOBYPASSRLS`.
  - goose migrations with an advisory lock; `current_workspace_id()` fails
    closed and is the groundwork for M2's RLS.
- **Binaries**: `api` (`serve | migrate up/down/status | healthcheck`) and
  `worker` (`serve | healthcheck`), sharing `internal/app`, with graceful
  shutdown.
- **Delivery**:
  - Distroless non-root images.
  - Hardened Compose stack (read-only, no caps, internal datastore network,
    API on loopback only), with Garage instead of MinIO (ADR 0003).
  - Makefile and CI (lint, security, race tests and coverage gates, fuzz
    smoke, image checks) plus Dependabot.
  - OpenAPI 3.1 spec, linted with Redocly and kept in sync with the router by
    a test.
- **Docs**: ADRs 0001–0003, `docs/deployment.md` (India residency, secrets,
  config reference), `docs/development.md`.

### Verification

- `make check`: fmt, vet, staticcheck, gosec (0 issues), govulncheck
  (0 called vulnerabilities), race tests, coverage core 100% and overall
  94.0%.
- Fuzz targets: `FuzzParseSize`, `FuzzLoadErrorsCarryNoInput`, `FuzzDecode`,
  `FuzzPrecheckIsSupersetOfPatterns`. The precheck fuzzer found a real
  redaction gap (phone numbers split by `\n`), which is now fixed and kept as
  a seed.
- `make fuzz-smoke`: all 4 targets clean.
- Benchmark (logx, ordinary access-log record): 0 allocs, ~4.4 µs. Before
  the profiling-guided optimisation it was 66 allocs and ~16 µs. Records with
  sensitive content: 8 allocs, ~11 µs.
- The full `make check` run caught a flaky shutdown bug: connections that
  never send a request made `Shutdown` exceed its deadline, so a SIGTERM
  could exit non-zero. It is now reproduced by a regression test and fixed
  (force-close after the drain timeout). `docker compose stop` exits 0.
- Stack:
  - All services healthy; migrate exits 0; `storage-init` is idempotent.
  - `/readyz` returns 503 while Redis is stopped and recovers after.
  - Containers run non-root with no shell (except Garage; see ADR 0003).
  - Redis ACLs are enforced.
  - Logs contain no secrets.
  - Inter-container networking was verified with host networking only,
    because of a Codespace iptables issue (see `docs/development.md`).

## Post-M1 hardening (2026-10-19)

- **TDD guard fail-open fixed.** On macOS (no `realpath -m`), or on any
  internal error, the guard exited 1 or 5. Claude Code treats that as
  non-blocking, so edits went through. The guard now:
  - uses a portable lexical `normpath`;
  - turns every unexpected exit into a block (exit 2);
  - is verified under bash 3.2.57 with BusyBox;
  - has its specs run in CI on macOS under `/bin/bash`.
- **Stack smoke test** (`make smoke`, CI job `stack-smoke`). CI run
  39190405578 on a normal Docker host passed everything:
  - production config (`compose.yaml` alone): 38/38;
  - dev overlay: 35/35.
- **Network isolation verified in production config**: postgres, redis and
  garage have no egress and no published ports; the api and worker do have
  egress.
- **First CI run (39190014692) found two things:**
  - The dev overlay gives the datastores egress, because it puts them on a
    routable network to publish ports. This is now a documented dev-only
    trade-off.
  - A false failure in my DNS-based check: Docker's embedded DNS does not
    answer probes that share another container's namespace. The probes now
    use TCP connects to IP literals.

## M2: Auth, tenancy, audit and email

Plan: `merry-cuddling-wren`. Decisions are recorded in ADRs 0004–0007.

Delivered:
- **Platform**:
  - `keyring`: AES-256-GCM, key IDs for rotation, HKDF sub-keys.
  - `clientip`: trusted-proxy `X-Forwarded-For` walk.
  - `ratelimit`: pure sliding-window core, an atomic Redis Lua adapter, and
    middleware. Auth classes fail closed; writes fail open.
  - `jobs`: River, with its migrations run by `api migrate up`.
  - `httpx` keyset pages: opaque cursors, bounded limits.
  - `postgres.InTx` / `InReadTx` with tenant and user GUCs.
  - Config: origin, sessions, proxies, keyring, SMTP. `COOKIE_SECURE` is
    removed and rejected.
- **Schema** (`00002_identity_tenancy.sql`):
  - users and separate credentials, recovery codes, email tokens;
  - workspaces, members, brands, brand roles, invitations;
  - append-only audit log.

  RLS covers every table, three narrow `SECURITY DEFINER` pre-auth
  functions handle lookups before login, and composite tenant FKs guard
  cross-tenant references (ADR 0006). Queries go through sqlc; `make
  sqlc-check` is part of `check`.
- **Audit**: writes are only possible inside `audit.Writer.InWriteTx`, which
  inserts the audit row in the same transaction. Client IPs are stored as an
  HMAC. There are workspace and account listings.
- **Email** (moved from M10):
  - pure templates and a MIME builder, fuzzed;
  - an SMTP adapter (STARTTLS, implicit TLS, or plaintext in dev only);
  - a River outbox with encrypted job args, enqueued in the write
    transaction;
  - Mailpit in dev (ADRs 0004 and 0007).
- **Auth** (`/v1/auth/*`, `/v1/me*`):
  - signup that creates a workspace and first brand, with
    anti-enumeration;
  - email verification, login with a dummy-hash timing guard, logout and
    logout-all;
  - password change, forgot and reset;
  - TOTP enrolment and disable, recovery codes, and an MFA login challenge.

  Passwords are argon2id (46 MiB, t=1, p=1) behind a concurrency semaphore.
  Sessions live in Redis in `__Host-` cookies; CSRF uses stdlib
  `CrossOriginProtection` (ADR 0005).
- **Workspaces** (`/v1/workspaces*`, `/v1/invitations/accept`):
  - workspaces and brands (archiving is owner only);
  - members and per-brand roles;
  - invitations, which are emailed, expire after 7 days, and need the
    invited verified email to accept;
  - the audit-log endpoint.

  The pure RBAC core covers owner, admin, publisher, agent and viewer.
  A workspace or brand you cannot see returns 404; a forbidden action in
  your own workspace returns 403. Writes are limited to 120/min per user.
- **Assembly**:
  - `app.BuildAPI` serves health, auth and workspaces behind CSRF
    protection, and `api serve` uses it.
  - The OpenAPI spec documents all 36 operations, is Redocly-clean, and is
    kept in sync with the router by a test.
  - `internal/apitest` is the end-to-end HTTP test harness.
- **Ops**:
  - Compose wires the keyring, the SMTP settings and an opt-in
    `smtp_password` secret.
  - The stack smoke test gains the account flow: signup → Mailpit → verify
    → login → brand → audit → cross-tenant 404.
  - `docs/deployment.md` covers SES `ap-south-1`, sessions and proxies.

### Verification

- **`make check`**: green twice in a row.
  - Checks: fmt, vet, sqlc-check, staticcheck, gosec (0 issues),
    govulncheck (0 called vulnerabilities), race tests.
  - Coverage: core 96.9%, overall 87.2% (gates: 90% and 80%).
- **HTTP tests** (`internal/apitest`, real Postgres, River and Redis over
  TLS). Every endpoint has:
  - 401 without a session;
  - 403 for an insufficient role;
  - a cross-tenant 404;
  - 422 validation errors and the canonical error shape;
  - audit-row assertions.
- **Fuzz**: `make fuzz-smoke FUZZTIME=10s` is clean across 30 targets,
  including one per request type.
  - It found a real bug in the email MIME builder. A non-ASCII display name
    ending in `\` produced an unparseable `To:` header, because stdlib
    Q-encoding leaves characters that are invalid in an atom. Fixed by
    B-encoding; the input is kept as a corpus regression.
- **Account flow, end to end**, against real binaries: datastores and
  Mailpit in Compose, `api` and `worker` as host processes, because of the
  Codespace bridge issue. 17/17 checks passed:
  - signup → SMTP delivery to Mailpit → verify → login → create brand →
    audit log shows `brand.create`;
  - the owner gets 200 and a second account gets 404 on the same workspace;
  - logout gives 401;
  - no email addresses in the logs.

  The full stack smoke test, in both modes, runs in CI (`stack-smoke`).
- **OpenAPI**: 36 operations, Redocly-clean, with a router-sync test.
- **Benchmarks** (2-vCPU Codespace):

  | Benchmark | Time/op | Allocs/op | Notes |
  | --- | --- | --- | --- |
  | `core.Can` (permission check) | 98 ns | 1 | |
  | Sliding-window `Decide` | 28 ns | 0 | |
  | Limiter `Allow`, in-memory store | 1.4 µs | 6 | |
  | Redis session `Get` | 119 µs | 12 | round trip to the container |
  | Audited write envelope (`InWriteTx`, no-op body) | 0.58 ms | 60 | BEGIN, GUCs, audit insert, COMMIT |
  | argon2id verify (production cost) | 30 ms | — | 46 MiB; `ARGON2_MAX_CONCURRENCY=4` caps memory at about 190 MiB |
  | logx access-log record | 3.3 µs | 0 | unchanged |
- **Test infrastructure flake, found and fixed**: 2 of 3 `make check` runs
  failed with `wait for reaper: context deadline exceeded`.
  - Cause: packages share one testcontainers reaper, which exits 10s after
    its last client. The last container-using packages arrived while it was
    exiting.
  - Fix: `make test` sets `RYUK_RECONNECTION_TIMEOUT=5m`.

### Verified on macOS (2026-10-22)

The Codespace could not run the Compose stack end to end (Docker bridge
traffic is dropped there). The `docs/handoff.md` checklist was completed on
a normal Docker host: Apple Silicon, Docker 29.9.1, Go 1.27.3.

- **`make check`**: green as-is. Coverage core 96.9%, overall 87.2%; gosec
  0 issues; govulncheck 0 called vulnerabilities.
- **Stack smoke, production config** (`compose.yaml` alone): 41/41,
  including datastore isolation (no egress, no published ports) and the
  three pre-verification account-flow checks.
- **Stack smoke, dev overlay** (`make up && make smoke`): 52/52, including
  all 17 account-flow checks. This is the first time the account flow ran
  inside Compose.
- **Manual Mailpit check**: the verification email arrives with text and
  HTML parts and a `…/verify-email#token=…` link.
- **Fix found on the way**: `scripts/stack-smoke.sh` called `timeout(1)`,
  which macOS lacks. `tcp_open` moved to `scripts/lib/net.sh` and now polls
  a background connect with a 5s deadline, using only bash 3.2 builtins and
  `sleep`. It is specified by `scripts/net_test.go` (red first, run in the
  `guard-macos` CI job), mutation-checked, and shellcheck-clean.

Still open: the first CI run of the account flow (`stack-smoke` job) after
pushing.

## Open questions and follow-ups

1. **Codespace firewall**: Compose bridge networks are dropped by stale
   iptables-legacy rules in this Codespace. A user-run fix and a
   host-process alternative are documented in `docs/development.md`.
   Network isolation is verified on a normal Docker host by the
   `stack-smoke` CI job.
2. **GO-2026-5932** (golang.org/x/crypto): resolved for M2. It affects only
   `x/crypto/openpgp`, which we do not import; `govulncheck` reports no
   called vulnerabilities.
3. **S3 client for M4**: there is no S3 SDK on the allowed list. Decide
   between a stdlib SigV4 signer and an SDK (ADR).
4. **Statement timeout** (15s) and idle-in-transaction timeout (30s) are
   constants. Make them configurable if analytics exports need longer
   queries.
5. **Garage runs as uid 0** (with no capabilities) because its scratch image
   has no user that owns the volumes. Revisit if upstream adds one.
6. **SMTP error text in River's attempt errors.** A provider reply such as
   `550 <addr>: user unknown` can echo the recipient into `river_job.errors`.
   Logs are redacted; the database column is not. Consider wrapping SMTP
   errors with only their status code.
7. **Bounces and complaints** (SES notifications) are not handled yet. They
   belong with email preferences.
8. **Non-browser clients** (mobile, public API) will need a bearer-token
   scheme alongside the cookie (ADR 0005).
