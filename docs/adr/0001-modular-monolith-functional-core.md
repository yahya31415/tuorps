# ADR 0001: Modular monolith with a functional core

- Status: accepted
- Date: 2026-10-19

## Context

Tuorps covers many domains (auth, publishing, inbox, WhatsApp, analytics,
billing…) that share one tenant model and one database. The team is small,
and correctness and auditability matter more than independent deployability.

## Decision

- One Go module and two binaries: `cmd/api` (HTTP) and `cmd/worker`
  (scheduler, webhook processing, sync jobs). They share `internal/app` as the
  composition root.
- Each domain lives in `internal/<domain>/` with:
  - `core/` for pure functions and immutable types: no I/O, clock, randomness
    or globals.
  - `ports.go` for function types the core needs.
  - `adapters/` for Postgres, Redis and platform clients.
  - `http/` for thin handlers.
- `internal/platform/` holds cross-cutting shell code: config, logging
  (redacting), httpx, clock, ids, postgres, redisx and lifecycle.
- Time and IDs are injected as `clock.Now` and `ids.NewID`.
- Prefer the standard library. Go 1.27 provides `uuid` (UUIDv7) and
  `encoding/json/v2`, which is stricter (duplicate keys, case-sensitive
  names, invalid UTF-8), so neither needs a dependency.

## Accepted exceptions to "no package-level variables"

- Sentinel errors (allowed by CLAUDE.md).
- `migrations.sqlFiles` (`embed.FS`): `//go:embed` requires a package-level
  variable. The value is read-only.

## Consequences

- Business rules are unit- and property-testable without containers.
  Adapters are tested against real Postgres/Redis via testcontainers.
- A module can be split into its own service later along its port boundary.
