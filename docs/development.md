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

`make red` accepts a compile failure only when every error location is inside
the target package, for example a test calling an undefined function or
importing a package that does not exist yet. Errors elsewhere are refused as
"unrelated".

## Integration tests

Adapter, migration and binary tests start Postgres 18.6 and Redis 8.8 through
testcontainers (`internal/platform/testinfra`), using the same role bootstrap
as production. They need a Docker daemon. They never call external APIs.

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
migrations, but not network isolation.
