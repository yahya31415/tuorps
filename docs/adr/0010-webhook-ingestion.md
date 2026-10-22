# ADR 0010: Webhook ingestion

- Status: accepted
- Date: 2026-10-22

## Context

Meta (Pages, and Instagram through Facebook Login), Instagram Login and
LinkedIn push events to us. CLAUDE.md requires signatures to be verified in
constant time before parsing, and verify → persist → enqueue → 200 in under
100 ms. M3 builds the base; the inbox (M5) consumes the events.

Facts from `docs/api-reference.md` ("Webhook verification and dedupe"):
- Each platform signs with a different secret and format:
  - Meta: `X-Hub-Signature-256` with the app secret.
  - Instagram Login: same header, but the docs do not say which secret.
  - LinkedIn: `X-LI-Signature` over `"hmacsha256=" + body` with the client
    secret.
- Only LinkedIn provides a notification ID. Meta retries for 36 h, LinkedIn
  for 8 h.
- Meta's data-deletion callback must answer with a confirmation code in the
  same HTTP response.

## Decision

- **One route per signing secret**, so the key is chosen by the route and
  never by parsing the body:
  - `/v1/webhooks/meta`, `/v1/webhooks/instagram`, `/v1/webhooks/linkedin`
    (GET for the endpoint check, POST for events);
  - `/v1/webhooks/{meta,instagram}/deauthorize`;
  - `/v1/webhooks/{meta,instagram}/data-deletion`.

  The Instagram routes accept a valid signature from **either** the
  Instagram app secret or the Meta app secret, each compared with
  `hmac.Equal`, until a real delivery in development mode shows which one
  Meta uses. Then the other is dropped.
- **Pipeline for every event POST**:
  1. Read the raw body, capped at 1 MiB.
  2. Verify the signature in constant time. On failure: 401, nothing stored
     or enqueued, a counter logged without the body.
  3. In one transaction, insert into `webhook_receipts (provider, dedupe_key)`
     with `ON CONFLICT DO NOTHING`. A new row means the River job
     `webhooks.process` is inserted with `InsertTx`; a duplicate means
     nothing is inserted.
  4. Return 200.

  `dedupe_key` is LinkedIn's `notificationId`, and SHA-256 of the raw body
  otherwise. Receipts are pruned after 7 days (longer than every retry
  window). After that a redelivery would be processed again, so
  **processing must be idempotent**.
- **No personal data at rest from ingestion.** The payload travels sealed in
  the job args (AAD `jobs/webhooks.process`), as with email (ADR 0004).
  `webhook_receipts` holds only the provider, the key and the time.
  `tuorps_app` may only INSERT into it; pruning goes through a
  `SECURITY DEFINER` function.
- **Routing**: the job looks up `connections_by_external(kind, external_id)`,
  which returns every `(connection, workspace)` that has the account. One
  Page can belong to an agency's workspace and the client's. The job
  handles each match in that workspace's scope. In M3 it acts only on:
  - deauthorize and permission-loss events, which set `needs_reauth` and
    send an email;
  - data deletion.

  Other events are recorded and left for M5.
- **Data deletion**: the handler verifies the `signed_request` (HMAC over
  the encoded payload, constant time, `algorithm` must be `HMAC-SHA256`),
  generates a random confirmation code, records it in
  `data_deletion_requests`, enqueues the job, and responds
  `{"url": APP_ORIGIN/data-deletion/<code>, "confirmation_code": <code>}`.
  The job wipes the tokens of every grant for that platform user (found
  through `grants_by_provider_user`) and marks the request done.
  `GET /v1/data-deletion/{code}` reports the status and nothing else.
- **Endpoint checks** (`hub.challenge`, LinkedIn `challengeCode`):
  - The verify token is compared in constant time.
  - `hub.challenge` is echoed only if it is short and numeric.
  - The LinkedIn response is computed with no I/O, well within its 3 s limit.
- **Cross-origin protection**: the webhook routes have no session, so
  `AddInsecureBypassPattern("/v1/webhooks/")` exempts them explicitly. This
  does not rely on platforms omitting `Origin`. A test sends a foreign
  `Origin`.
- **Rate limit**: class `webhook_ip`, which fails open (a Redis outage must
  not lose events). The limit is high, sized for platform bursts.

## Consequences

- M5 adds event handling inside `webhooks.process` without touching
  ingestion.
- A slow or failing consumer never delays the 200; failures retry in River.
- If a secret leaks, rotate it in the app dashboard and in Docker secrets.
  The two-secret Instagram check makes overlap during a rotation easy.

## Amendments (2026-10-22, during implementation)

- **Dedupe key.** Every provider uses the SHA-256 of the event kind and the
  raw body, LinkedIn included. LinkedIn's `notificationId` repeats once per
  subscriber, so it does not identify one delivery.
- **Per-account subscriptions move to M5.** The fields to subscribe depend
  on inbox features. The Instagram-via-Facebook field and the LinkedIn
  person URN are unconfirmed or unavailable with M3's scopes. M3 ships the
  endpoints, verification, dedupe and enqueueing, and the app-level
  deauthorize and data-deletion callbacks; their handling (re-auth, token
  wipe, request completion) runs in `webhooks.process`.
- Measured: about 1.2 ms per delivery end to end (`BenchmarkMetaWebhook`).
