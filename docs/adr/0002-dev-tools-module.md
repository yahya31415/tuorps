# ADR 0002: Pin developer tools in a separate tools module

- Status: accepted
- Date: 2026-10-19

## Context

`make check` needs exact, reproducible versions of the dev tools:

- sqlc v1.31.1
- goose v3.29.0
- staticcheck v0.8.2
- gosec v2.29.0
- govulncheck v1.9.0

Adding them with `go get -tool` to the main `go.mod` would pull hundreds of
tool-only dependencies into the application's module graph.

## Decision

The tools are pinned with `tool` directives in `tools/go.mod`, a separate
module, and invoked as `go tool -modfile=tools/go.mod <tool>` (`$(TOOL)` in
the Makefile). Dependabot watches `/tools` as well as `/`.

## Consequences

- The application `go.mod` lists only runtime and test dependencies.
- The first `go tool` run compiles the tools (sqlc needs cgo/gcc). After that
  they are cached.
