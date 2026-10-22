# Handoff: from the Codespace to a normal Docker host (macOS)

Status at handoff (2026-10-22): M2 is complete and committed. `make check`
passed twice in a row, and fuzz smoke is clean. Everything below is what the
Codespace **could not** verify, and why. Do these first on the Mac, before
starting M3. Read `CLAUDE.md` and `docs/progress.md` first.

## Why the Codespace was limited

The Codespace has a stale **iptables-legacy** `FORWARD DROP` ruleset next to
Docker's nftables rules, which silently drops traffic **between containers**
on Compose bridge networks.

| Works | Broken |
| --- | --- |
| Published ports from the host (testcontainers, `127.0.0.1:…`) | api → postgres or redis by service name; worker → mailpit |

The firewall fix needs `sudo iptables-legacy …`, which was not run (see
`docs/development.md`). Because of this:

- **The full Compose stack never ran end to end in the Codespace.** The M2
  account flow was verified with the datastores and Mailpit in Compose but
  `api` and `worker` as host processes (17/17 checks; method in
  `docs/development.md`).
- **The stack smoke test has not run since M2 changed it.** The last full
  run was CI 39190405578 (M1 only). The new "account flow" section
  (signup → Mailpit → verify → login → brand → audit log → cross-tenant 404
  → logout) has never run inside Compose.
- Network isolation (datastores have no egress in the production config) was
  only ever proven in CI, never locally.

None of this applies on Docker Desktop for Mac. Every pinned image (postgres,
redis, garage, mailpit, busybox, golang, distroless) publishes `linux/arm64`,
so Apple Silicon runs them natively.

## To do on the Mac, in order

1. **Prerequisites**: Go 1.27.x, Docker Desktop (running), `jq`, and
   GNU `timeout`: `brew install jq coreutils`.
   - `scripts/stack-smoke.sh` calls `timeout` in `tcp_open`, and macOS has
     no `timeout`. Either make `tcp_open` portable (preferred; keep the
     script working on Linux CI as well), or put Homebrew's `gnubin` on
     `PATH`.
   - The TDD guard and `make red`/`make green` are already portable to macOS
     bash 3.2 (CI runs their specs on macOS).
2. **`make check`**: it should pass as-is.
   - If a test fails with `wait for reaper … context deadline exceeded`, see
     the `RYUK_RECONNECTION_TIMEOUT` note in the Makefile's `test` target.
3. **Stack smoke, production config** (no dev overlay, so there is no
   Mailpit; the account flow stops at "signed up, not verified"):

   ```sh
   make dev-secrets
   export SMTP_HOST=smtp.example.invalid MAIL_FROM='Tuorps CI <no-reply@tuorps.example>'
   docker compose -f compose.yaml up -d --build --wait
   COMPOSE="docker compose -f compose.yaml" scripts/storage-init.sh
   COMPOSE="docker compose -f compose.yaml" scripts/stack-smoke.sh
   ```

   Expect every check to pass, including "postgres/redis/garage cannot reach
   the internet" and the three account-flow checks.
4. **Stack smoke, dev overlay** (the full account flow through Mailpit):

   ```sh
   make up && make smoke
   ```

   Expect 17 account-flow checks among the rest.
5. **Manual check** from the M2 plan: with `make up`, sign up through the
   API, then open <http://127.0.0.1:8025> and confirm the verification email
   is there, with a `…/verify-email#token=…` link.
6. **CI**: after pushing, watch the `stack-smoke` job. It runs both modes on
   ubuntu-24.04, and this is the first run of the new account flow there.

## Gotchas when re-running the smoke test

- **Signup is limited to 5 per hour per client IP**, and the limit lives in
  Redis, which keeps its volume. One production run plus one dev run uses 3
  signups, so a second full cycle within the hour will fail with 429.
  `docker compose -f compose.yaml -f compose.dev.yaml down -v` resets it
  (this deletes all local data). `make down` keeps volumes.
- On Docker Desktop, every request reaches the API from the VM's gateway
  address, so all smoke clients share one IP for rate limiting.
  `TRUSTED_PROXY_CIDRS` is unset in Compose, which is correct here.
- `scripts/dev-secrets.sh` writes `secrets/smtp_password` as a placeholder.
  It is ignored unless `SMTP_PASSWORD_FILE` is set.
- `secrets/` is gitignored, so it does not travel with the repo. Run
  `make dev-secrets` on the Mac.

## After that

- Fix anything the steps above uncover: red test first, per `CLAUDE.md`.
  Record the results in `docs/progress.md`, under M2 → Verification.
- Then start **M3: account connections and token vault**. Read
  `docs/features.md` and `docs/api-reference.md`, and plan before coding.
  Open follow-ups to keep in mind are in `docs/progress.md` (SMTP error text
  in `river_job.errors`, bounces, a bearer-token scheme, and the S3 client
  ADR before M4).
