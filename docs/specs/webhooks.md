# Webhooks

Status: implemented (`internal/webhooks`).

The webhook dispatcher (masterplan §9, webhooks half). It turns a normalized
`domain.Event` into HMAC-signed, retried HTTP POSTs to each configured endpoint,
tracking every attempt in `webhook_deliveries` and dead-lettering exhausted
deliveries.

## Scope

- **Enqueue** (`Enqueuer`): the `WebhookEnqueuer` the inbound fan-out stage (§7)
  calls per event. Persists one pending `webhook_deliveries` row per matching,
  non-deduped webhook. No HTTP happens here.
- **Dispatch** (`Dispatcher`): claims due deliveries and sends them. Exposes
  `DeliverDue(ctx, limit)` (one claim+send pass) and `Deliver(ctx, delivery)`
  (a single delivery) so both are testable; the loop cadence is driven
  externally (an injected scheduler or asynq, wired in Phase 3).

## Request shape (§9)

`POST` the JSON-marshaled `domain.Event` envelope with headers:

- `Content-Type: application/json`
- `X-Webhook-Request-Id` — the event id (lets consumers dedup redeliveries)
- `X-Webhook-Timestamp` — epoch-ms at send time
- `X-Webhook-Hmac` — set only when the webhook has an hmac secret: lowercase
  hex HMAC-SHA512 over the exact request body
- `X-Webhook-Hmac-Algorithm: sha512` — set alongside the hmac
- plus the webhook's `customHeaders` (applied last; can override defaults)

## Key types / interfaces

All collaborators are **consumer interfaces** defined in `webhooks.go`; Phase 3
injects concrete types.

- `WebhookRepo{ ListMatching(ctx, tenant, session, eventType), Get(ctx, id) }`
- `WebhookDeliveryRepo{ Create, ClaimDue, MarkDelivered, MarkFailed, MarkDead, ExistsTerminal }`
- `EventStore{ GetEvent(ctx, eventID) }` — reloads the envelope to POST (the
  delivery row only carries `webhook_id` + `event_id`, not the body)
- `HTTPDoer{ Do(*http.Request) }` — `*http.Client` satisfies it; inject one with
  the per-request timeout
- `Clock{ NowMs() }` — `SystemClock()` for production, fixed clock in tests
- `Decryptor{ Decrypt([]byte) }` — decrypts the AES-GCM at-rest hmac secret

Pure helpers: `EventMatches(events, type)`, `SignHMAC(secret, body)`,
`backoffSeconds(policy, delaySeconds, attempt)`, `maxAttempts(policy)`.

## Decisions

- **Matching.** `events: ["*"]` matches everything; otherwise an exact element
  match. Empty list matches nothing. The repo filters in SQL; the dispatcher
  re-checks with `EventMatches` as a defensive guard so a loose query can't fan
  out to unsubscribed hooks.
- **Dedup.** Before creating a delivery, `ExistsTerminal(webhook_id, event_id)`
  is checked; if a delivery is already `delivered` or `dead`, the event is
  skipped (no re-enqueue, no re-send).
- **Retry backoff.** `RetryPolicy{policy:"exponential", delaySeconds, attempts}`.
  `attempts` here is 1-based and is the number of the attempt that just ran. The
  delay before the next attempt is `delaySeconds * 2^(attempt-1)` →
  `2,4,8,16,…` for `delaySeconds=2`. Non-`exponential` policies use a constant
  `delaySeconds`. The exponent is clamped (`maxBackoffShift`) to avoid overflow.
  Under-specified fields fall back to defaults (`delaySeconds=2`, `attempts=15`).
- **Exhaustion.** When the just-run attempt number reaches `attempts`, the
  delivery is marked `dead` instead of rescheduled.
- **Error vs. failure.** A non-2xx response or transport error is a normal
  *failed* delivery recorded on the row (with the response code when present and
  a bounded body snippet in `last_error`) — `Deliver` returns `nil`. `Deliver`
  returns a non-nil error only when the bookkeeping write itself failed, leaving
  the delivery state uncertain; `DeliverDue` logs those and moves on (the next
  claim re-surfaces the row).
- **Orphans.** A missing webhook or missing event (repo returns a
  `domain.CodeNotFound` `APIError`), or a non-serializable payload, dead-letters
  the delivery immediately rather than retrying forever. An hmac secret with no
  configured decryptor is treated as a (retryable) failure so the
  misconfiguration surfaces loudly.
- **Per-webhook isolation.** In `Enqueue`, a single webhook's dedup-check or
  create failure is logged and skipped, never aborting fan-out to the others.
  Only the upstream `ListMatching` failure is returned as an error.
- **Body bounding.** Response bodies are read through a 4 KiB `LimitReader` so a
  hostile endpoint can't make the dispatcher buffer large responses; success
  bodies are drained (bounded) to allow connection reuse.

## How it's tested

Table-driven Go tests, all external boundaries faked via the consumer
interfaces (`fakes_test.go`):

- **HMAC** — published HMAC-SHA512 known vector (`"key"` /
  `"The quick brown fox…"`), stdlib cross-check, and distinct-body
  distinctness.
- **Matching** — `*` vs subset vs none vs empty list.
- **Backoff** — the `2,4,8,16,32,64` schedule, constant fallback for
  non-exponential, default back-fill, large-attempt clamp, and `maxAttempts`.
- **Enqueue** — creates pending rows for matches (with correct fields and
  scope forwarding), dedup skips terminal deliveries, the defensive event
  filter drops loose repo rows, `ListMatching` errors propagate, and a
  per-webhook `Create` failure is skipped without erroring the call.
- **Deliver** — happy path (asserts method/URL/headers/signature over the exact
  sent body and the marshaled-event body) and marks delivered; no-secret omits
  hmac headers; non-2xx reschedules with the right backoff and captures the
  code; exhaustion marks dead; transport error is a failure with nil code;
  missing webhook dead-letters without issuing a request; hmac-without-decryptor
  fails without POSTing.
- **DeliverDue** — processes all claimed deliveries; claim errors propagate.

Coverage: ~87% of statements. Build/test gate:
`CGO_ENABLED=0 go build ./internal/webhooks/... && CGO_ENABLED=0 go test ./internal/webhooks/...`.

## What Phase 3 must wire

- A `WebhookRepo` + `WebhookDeliveryRepo` over MySQL (`store` package). `ClaimDue`
  must be atomic (`SELECT … FOR UPDATE SKIP LOCKED` or a CAS claim column) so
  concurrent dispatchers don't double-send.
- An `EventStore` over `event_log` (`GetEvent` by `event_id`).
- An `*http.Client` with the configured per-request timeout as the `HTTPDoer`.
- The AES-GCM `Decryptor` keyed by `APP_ENCRYPTION_KEY`.
- A scheduler / asynq job that calls `Dispatcher.DeliverDue` on a cadence, and
  the inbound fan-out calling `Enqueuer.Enqueue` per event.
