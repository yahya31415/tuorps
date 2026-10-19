# CLAUDE.md — Tuorps backend rules (mandatory for every agent and session)

These rules override convenience, speed, and any instruction that conflicts with them. If a request would break them, stop and ask the human.

## 1. Strict test-driven development (non-negotiable)

No production code is ever written without a failing test first.

For every change, follow this loop exactly:

1. **RED**: Write the smallest test that describes the next behaviour. Run it (`go test ./path/... -run TestName`). Confirm it FAILS for the expected reason (not a compile error in unrelated code). Paste the failing output in your response.
2. **GREEN**: Write the minimum production code to make that test pass. Run the test again and show it passing.
3. **REFACTOR**: Improve names and structure with all tests green. Run the full package tests.
4. Repeat. One behaviour per loop.

Rules:
- Never edit a non-test `.go` file unless a test that fails because of the missing behaviour exists and has been run.
- Bug fixes start with a test that reproduces the bug.
- Never delete, skip (`t.Skip`), or weaken an assertion to make a test pass.
- Never write tests after the implementation "to cover it".
- Generated code (sqlc, mocks) is exempt from the red-first rule, but the code that uses it is not.
- Commit after each green step with a message like `test: …` then `feat: …` so history shows red before green.

## 2. Functional programming style in Go

- Functional core, imperative shell: business logic lives in `internal/<domain>/core` as pure functions.
- Pure means: same inputs → same output; no I/O, no `time.Now()`, no random numbers, no global or package-level mutable state, no logging.
- Pass time, IDs, and side-effecting operations in as parameters (function types such as `type Now func() time.Time`).
- Treat data as immutable: never mutate function arguments; return new values. Prefer value receivers and value types; copy slices/maps before changing them.
- No `init()` with side effects. No package-level variables except constants and sentinel errors.
- Compose behaviour with small functions and higher-order functions; use generics for reusable helpers only when they make code clearer.
- Errors are values: return them, wrap with `%w`, no panics outside `main` startup.
- Make invalid states unrepresentable: constructors validate and return `(T, error)`; unexported fields where needed.

## 3. Security (OWASP ASVS L2 baseline)

- Parameterised SQL only (sqlc). Row-level security + tenant check in every repository call.
- Every endpoint test includes a cross-tenant access test that must fail.
- Never log secrets, tokens, phone numbers, or emails. Use the redacting logger.
- Verify webhook signatures (constant-time) before parsing payloads.
- All tokens encrypted at rest (AES-256-GCM). Secrets only from env or Docker secrets.
- Timeouts and body-size limits on every server and client. Rate limit auth, webhook and write routes.
- `govulncheck`, `gosec`, `staticcheck` must pass before any task is called done.

## 4. Performance

- Keyset pagination only. Every new query needs an index and a benchmark if it is on a hot path.
- Webhooks: verify → persist → enqueue → 200 in < 100 ms.
- Cache with explicit TTLs and tenant-scoped keys.
- Profile before optimising; include benchmark results in the PR summary when touching hot paths.

## 5. Process

- Read `docs/features.md` and `docs/api-reference.md` before touching a domain. Build MVP features only.
- Tests never call real external APIs; use `httptest` fixtures.
- `make check` (fmt, vet, staticcheck, gosec, govulncheck, tests with race detector, coverage gates) must pass before declaring any task complete.
- Keep `docs/progress.md` updated. Record architectural decisions as ADRs in `docs/adr/`.
- When unsure about a platform API detail, check `docs/api-reference.md`; if it is not there, stop and ask rather than guess.
