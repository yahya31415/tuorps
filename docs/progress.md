# Progress

## Status

| Milestone | State |
| --- | --- |
| Step 0: CLAUDE.md + TDD guard | ✅ done |
| M1 Foundation | ✅ done (2026-10-19) |
| M2 Auth and tenancy | next |
| M3 Account connections and token vault | — |
| M4 Publishing | — |
| M5 Unified inbox | — |
| M6 WhatsApp suite | — |
| M7 Analytics | — |
| M8 Approvals and agencies | — |
| M9 Billing and compliance | — |
| M10 Extras | — |

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

## Open questions and follow-ups

1. **Codespace firewall**: Compose bridge networks are dropped by stale
   iptables-legacy rules in this Codespace. A user-run fix is documented in
   `docs/development.md`. Network isolation is verified on a normal Docker
   host by the `stack-smoke` CI job (passing).
2. **GO-2026-5932** (golang.org/x/crypto, no fix yet): currently not
   reachable from our code. M2 imports x/crypto for argon2id. Check whether
   the affected package is used before then.
3. **S3 client for M4**: there is no S3 SDK on the allowed list. Decide
   between a stdlib SigV4 signer and an SDK (ADR).
4. **Statement timeout** (15s) and idle-in-transaction timeout (30s) are
   constants. Make them configurable if analytics exports need longer
   queries.
5. **Garage runs as uid 0** (with no capabilities) because its scratch image
   has no user that owns the volumes. Revisit if upstream adds one.
