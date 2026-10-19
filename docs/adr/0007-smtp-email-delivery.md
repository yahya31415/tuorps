# ADR 0007: Provider-agnostic SMTP for email delivery

- Status: accepted
- Date: 2026-10-19

## Context

M2 needs transactional email (ADR 0004). Provider HTTP SDKs are not on the
allowed dependency list, and choosing one would lock in a vendor. Mail
content (addresses, links) is personal data under the DPDP Act, so the
provider's region matters.

## Decision

- Deliver over **SMTP** with stdlib `net/smtp`, in
  `internal/email/adapters`:
  - dial and I/O timeouts (`SMTP_TIMEOUT`, 10s);
  - `SMTP_TLS=starttls` (port 587, the default) or `implicit` (port 465),
    with TLS 1.2 or later and certificate verification;
  - `none` only when `TUORPS_ENV=dev`, for Mailpit.

  AUTH PLAIN is used only over TLS.
- Messages are built by the pure `email/core`:
  - text and HTML parts from `text/template` and `html/template`;
  - templates keyed by locale (English now; Indic locales later);
  - RFC 5322/MIME output with header-injection-safe encoding, fuzzed.

  Links are built on `APP_ORIGIN`, with tokens in the URL **fragment**
  (`#token=`), so they never reach server logs or `Referer` headers.
- Recommended production provider: **Amazon SES SMTP in `ap-south-1`
  (Mumbai)**, for India residency. ZeptoMail and Postmark also work, with no
  code change.
- Development and tests capture mail in **Mailpit** (pinned by digest,
  `compose.dev.yaml`, UI and API on `127.0.0.1:8025`). Tests never contact a
  real provider.
- The SMTP password comes from the Docker secret `smtp_password`, through
  `SMTP_PASSWORD_FILE`.

## Consequences

- Only the worker needs SMTP settings. The API only enqueues.
- Bounce and complaint handling (SES notifications) is not in M2. It will
  arrive with the email preference work.
- Moving to a provider HTTP API later needs only a new `Sender` adapter.
