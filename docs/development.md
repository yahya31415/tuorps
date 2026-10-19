# Development notes

## TDD workflow and the guard

`.claude/settings.json` runs `scripts/tdd-guard.sh` before every
Edit/Write/MultiEdit. Edits to non-test `.go` files are blocked unless
`make red` recorded a failing test in the last 30 minutes. Edits to
`*_test.go`, `testdata/`, `docs/`, `migrations/`, generated code and
non-Go files are always allowed.

A Bash hook blocks any shell command that mentions `.tdd` unless it is a
plain read (`cat`, `ls`, `head`, `tail`, `stat` without pipes or
redirection). The check is deliberately coarse: a heredoc whose text merely
mentions the directory is blocked too. Write such files with the editor tools.

The loop, in practice:

```sh
# 1. write the failing test
make red PKG=./internal/x/core RUN='^TestThing$'   # records state only if it fails for a valid reason
# 2. implement until `go test ./internal/x/core` passes
# 3. refactor (still inside the window)
make green                                         # target test + whole package with -race; clears the state
git commit (test: …) && git commit (feat: …)
```

`make green` closes the window. Run it **after** refactoring: a later
change, even a pure refactor, needs a new failing spec, such as an
allocation budget or a property.

The scripts are portable to macOS: bash 3.2 (`/bin/bash`), BSD userland,
no GNU `realpath`. Paths are normalised lexically by `scripts/lib/path.sh`.
The guard **fails closed**: any unexpected error exits 2 (block), because
Claude Code treats every other exit code as a non-blocking error. CI runs the
guard specs on macOS under `/bin/bash` (`TDD_SCRIPT_SHELL=/bin/bash`).

`make red` accepts a compile failure only when every error location is inside
the target package, for example a test calling an undefined function or
importing a package that does not exist yet. Errors elsewhere are refused as
"unrelated".

## Integration tests

Adapter, migration and binary tests start Postgres 18.6 and Redis 8.8 through
testcontainers (`internal/platform/testinfra`), using the same role bootstrap
as production. They need a Docker daemon. They never call external APIs.

## HTTP API tests

`internal/apitest` runs the whole API (`app.BuildAPI`) over testcontainers
Postgres (migrated, plus River's schema) and Redis, behind
`httptest.NewTLSServer`, so the `__Host-` cookie works. It provides:

- `Client()`: a browser-like client with its own cookie jar and client IP
  (via `X-Forwarded-For` from a trusted loopback proxy);
- `SignUp(email, workspace)`: an account that is signed up, verified and
  logged in;
- `Emails()` / `LastEmailTo()` / `TokenFrom()`: queued email, decrypted from
  River's job table.

Every endpoint test includes 401, 403, a cross-tenant 404, validation errors
and audit assertions. Each request type also has a decoder fuzz target
(`httpxtest.FuzzDecode`).

## Stack smoke test

`make smoke` (after `make dev-secrets up storage-init`) checks the running
stack end to end:

- probes and migrations;
- service DNS;
- api/worker egress;
- that postgres, redis and garage **cannot** reach the internet;
- dev-overlay ports;
- non-root, read-only, no-shell and no-capabilities hardening;
- Redis ACLs and anonymous S3 refusal;
- `/readyz` during a Redis outage;
- secrets absent from logs;
- the account flow: signup returns 202, login before verification returns
  403, and anonymous requests return 401. With the dev overlay it continues:
  - the verification email is read from Mailpit's API;
  - verify, log in, create a brand;
  - the audit log shows `brand.create`;
  - a second account gets 404 on the first workspace;
  - logout ends the session;
- graceful SIGTERM exits.

The script detects its mode from the stack:

- **Production config** (`compose.yaml` alone): datastores must have no
  egress and no published ports.
- **Dev overlay** (`make up`): datastore ports must be published on loopback.
  Isolation is not asserted, because the overlay attaches the datastores to a
  routable network to publish ports, which also gives them egress. This is a
  deliberate dev-only trade-off.

The CI job `stack-smoke` runs both modes on a normal Docker host. Network
probes use TCP connects to IP literals: Docker's embedded DNS does not answer
a probe container that joins another container's network namespace.

## Known environment issue: GitHub Codespaces bridge networking

In this Codespace, a stale **iptables-legacy** ruleset (`FORWARD` policy
`DROP`, only `docker0` allowed) coexists with Docker's nftables rules. It
silently drops traffic between containers on user-defined bridge networks,
so the Compose services cannot reach each other (connections time out).
Testcontainers is unaffected because it connects through published ports.

Fix (run it yourself; it changes the Codespace firewall until restart):

```sh
sudo iptables-legacy -I FORWARD 1 -i br-+ -j ACCEPT
sudo iptables-legacy -I FORWARD 1 -o br-+ -j ACCEPT
```

Without it, the stack can be exercised with every service on
`network_mode: host`. That checks images, secrets, healthchecks and
migrations, but not network isolation. In that mode `make smoke` reports
exactly 4 failures: service DNS plus the three "cannot reach the internet"
checks. This is the expected negative control showing the isolation checks
detect missing isolation.

A lighter option for the account flow alone, as used to verify M2:
- run `postgres`, `redis` and `mailpit` in Compose (dev overlay, plus
  Mailpit's SMTP port `1025` published on loopback);
- run `api migrate up`, `api serve` and `worker serve` as host processes,
  with the secrets' hostnames rewritten to `127.0.0.1`;
- run the "account flow" section of `scripts/stack-smoke.sh`.

Published ports work here; only container-to-container traffic is dropped.
