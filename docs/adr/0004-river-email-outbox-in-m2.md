# ADR 0004: River in M2 as the transactional email outbox

- Status: accepted
- Date: 2026-10-19

## Context

The milestone plan introduced River (Postgres-backed jobs) in M3 and email in
M10. M2's signup, verification, password reset, invitations and security
notices all need email.

Sending mail inline from a request handler has three problems:

- A slow or failing SMTP provider stalls requests.
- If the transaction rolls back after the send, someone gets an email about
  something that never happened.
- If the transaction commits but the send fails, the email is silently lost.

## Decision

- Move River to M2 (`internal/platform/jobs`). `api migrate up` applies
  River's schema after goose, as the schema owner. The worker binary runs
  River.
- Email is a River job of kind `email.send`. It is inserted with
  `InsertTx` **in the same transaction** as the write that causes it
  (`email.Outbox.Enqueue(ctx, audit.Tx, msg)`). River's job table is
  therefore the transactional outbox: the email exists if, and only if, the
  write committed.
- Job args hold one-time tokens (verify, reset and invitation links), so
  they are **encrypted**: AES-256-GCM through the keyring, with AAD
  `jobs/email.send`. Job args in the table, in backups and in River's UI
  never show a token or an address in clear. One exception: River records
  each failed attempt's error, and an SMTP reply such as `550 <addr>: user
  unknown` can echo the recipient (see the progress.md follow-ups).
- The worker retries with River's backoff and applies a per-send timeout.
  Payloads it cannot decrypt or render are cancelled rather than retried.
  The worker logs nothing itself. River logs job errors through the
  redacting logger.

## Consequences

- One job system from M2 onwards. M3 adds token-refresh jobs to the same
  worker.
- Delivery is at-least-once: a crash after the SMTP send but before River
  marks the job complete re-sends. That is acceptable for these emails.
- Losing the keyring key that sealed a queued job makes that job
  undeliverable. Key rotation keeps old keys for reading (`ENCRYPTION_KEYS`
  lists them all).
