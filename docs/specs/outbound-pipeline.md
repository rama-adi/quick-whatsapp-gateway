# Outbound Pipeline

Package: `internal/wa/outbound` · Masterplan §10 + §13 send bodies.

The unified send pipeline. Translates a `domain.SendRequest` (the single
discriminated send body) into WhatsApp sends, with idempotency, per-session rate
limiting, optional jittered pacing, and a sync/async split.

## Scope

- Unified typed send: `text`, `poll`, `location`, `contact`. Poll sends support
  `selectableCount`, optional `pollEndTime` (epoch-ms close time), and
  `pollHideVotes`.
- Message sub-resource ops (§13): `reaction`, `edit`, `revoke`, `vote`, `forward`.
- Media sends (`image|video|audio|document|sticker`): the file is supplied inline
  as base64 in `media.data` (no server-side URL fetch). The adapter `Upload`s the
  bytes to WhatsApp and builds the matching message (caption on image/video/
  document, filename on document; `replyTo`/`mentions` become `ContextInfo`).
  Decoded size is capped at `MaxMediaBytes` (16 MiB). **Media bytes are never
  retained**: they live on the `outbox` row only until the send is dispatched,
  and the store strips `media.data` from the payload once the row is marked
  `sent` (kept on `failed` so the async worker can retry).
- Sync mode (default): block on the whatsmeow ack, return
  `{waMessageId, status, timestamp}`.
- Async mode (`?async=true`): persist a queued `outbox` row, return its id; the
  final status arrives later via a `message.status` event when the async worker
  drains the row.
- Idempotency: an **organization-scoped** `Idempotency-Key` (keyed by `organization_id`, §7/§10); a replay returns the original
  result with `replayed:true` and makes no new WhatsApp call.
- Rate limiting: per-session token budget in Redis (`rate_per_min` /
  `rate_per_hour`); sync over-limit → `rate_limited` error; async over-limit →
  defer (the row stays queued).
- Message recording: every **successful** dispatch (sync or async-drained) is
  written to the `messages` table as a `from_me` / `direction='out'` /
  `status='sent'` row, so the gateway's own sends show up in chat history.
  whatsmeow does not echo a self-authored send back as an `events.Message` on the
  same device, so without this the inbound pipeline never sees it and the only
  trace is the transient `outbox` row.
- Poll close recap: successful poll sends also upsert the `polls` metadata and
  schedule the optional close time into Redis. MySQL remains the durable source:
  the recap worker claims `polls.recap_emitted_at` before emitting `poll.recap`.

## Key types

- `Sender` (sender.go) — the pipeline. `NewSender(wa, outbox, limits, clock,
  opts...)`. Options: `WithPacing(max)`, `WithLogger(l)`,
  `WithMessageRecorder(r)`.
  - `Send(ctx, sess, req, opts) (SendResult, error)` — validate → idempotency
    replay → async-persist OR sync-dispatch.
  - `SendOp(ctx, sess, OpRequest) (SendResult, error)` — synchronous message ops,
    rate-limited like sends.
  - `Dispatch(ctx, req)` — exported, rate-limit/idempotency-free router the async
    outbox worker reuses to drive a persisted request to whatsmeow.
- `SendResult` — `{Mode, WAMessageID, Status, Timestamp, OutboxID, Replayed}`.
- `SendOptions` — `{Async bool, IdempotencyKey string}`.
- `OpRequest` / `MessageOp` (validate.go) — `reaction|edit|revoke|vote|forward`.
- `IsRateLimited(err)` — helper for the HTTP edge to map to 429.

## Consumer interfaces (defined here; wired by Phase 3)

All collaborators are small interfaces owned by this package — no sibling
`internal/*` imports.

- `WAClient` — narrow slice of whatsmeow: `SendText`, `SendPoll`, `SendLocation`,
  `SendContact`, `SendMedia` (image/video/audio/document/sticker via `Upload` +
  the matching message), `React`, `Edit`, `Revoke`, `Vote`, `Forward`. Each
  returns `(waMessageID string, ts int64, err error)`, `ts` in epoch-ms.
- `OutboxRepo` — `Insert`, `GetByIdempotencyKey`, `UpdateStatus`, `ClaimQueued`.
  `Insert` MUST enforce `(organization_id, idempotency_key)` uniqueness and return a
  conflict-coded error on duplicates (the pipeline falls back to a replay).
- `RateLimiter` — `Allow(ctx, sessionID, perMin, perHour) (ok, retryAfter, err)`.
- `Clock` — `NowMs() int64`. `SystemClock()` is the production impl.
- `MessageRecorder` — `RecordSent(ctx, SentMessage) error`. Optional (wired via
  `WithMessageRecorder`; nil disables recording). The `service`-package adapter
  (`MessageRecorderAdapter`) upserts the chat then the message via the store
  repos, keyed by `(session_id, wa_message_id)` — the **same** idempotent key the
  inbound pipeline uses, so a later echo or receipt reconciles onto the row
  instead of duplicating it.

## Message recording (the bot's own sends)

Recording happens inside the private `dispatch()` — the single chokepoint both
the sync front-door and the async worker (via the exported `Dispatch`) funnel
through, so every successful send is recorded exactly once. The row carries
`from_me=true`, `direction='out'`, `status='sent'`, the recipient as `chat_jid`,
and the body/quote/mentions from the request (`type` carries the rest). Sender
identity columns are left NULL (own messages have no resolved sender name).

Recording is **best-effort**: a recorder error is logged and swallowed, never
returned. The WhatsApp send already succeeded, so surfacing the error would both
mis-report the API result and flip the `outbox` row to `failed` (tripping a
needless retry). It is also skipped when no recorder is wired or the session id /
wa-message id is unknown (e.g. a unit-test dispatch with a fake client).

## The real adapter (waclient.go)

`NewWhatsmeowClient(*whatsmeow.Client) WAClient` is the only file importing
whatsmeow. It maps each method to the recon §7 path:

- text → `Conversation`, or `ExtendedTextMessage{ContextInfo}` when a reply or
  mentions are present.
- poll → a `PollCreationMessage` with option hashes, selectable count, optional
  end time, and hidden-vote privacy flag; location → `LocationMessage`; contact →
  `ContactMessage` (vcard verbatim, else built from name/phone).
- media → `Upload(bytes, MediaType)` then the matching `ImageMessage`/
  `VideoMessage`/`AudioMessage`/`DocumentMessage`/`StickerMessage` (sticker uploads
  under the image key set). Mimetype is `http.DetectContentType`'d when omitted.
- reaction → `BuildReaction`; edit → `BuildEdit`; revoke → `BuildRevoke`;
  vote → `BuildPollVote` (needs a synthesized poll `MessageInfo`).
- All dispatched via `SendMessage`; `SendResponse.Timestamp` is `.UnixMilli()`d.

Empty sender JID maps to `types.EmptyJID` (whatsmeow's convention for your own
outgoing message in react/revoke).

**Forward limitation:** whatsmeow has no build-forward helper and the pipeline
does not carry the original message content. The adapter sends a forwarded-tagged
`ExtendedTextMessage` referencing the source id. A faithful forward (copying the
original body / re-uploading media) is a Phase 3 task once a fetch-message-by-id
path exists.

## Session routing (the real send path)

The `Sender` is account-global (one instance), but the live whatsmeow clients are
per-session and owned by `wa.Manager`. The `WAClient` interface methods take no
session id, so the `Sender` carries the target session on the request context:

- `Send`/`SendOp` stamp `outbound.WithSessionID(ctx, sess.ID)` before any
  dispatch; the async outbox worker stamps `entry.SessionID` before
  `Sender.Dispatch`.
- `service.RoutingWAClient` (the production `WAClient`) reads the session id back
  with `outbound.SessionIDFromContext`, resolves the live `*whatsmeow.Client` via
  `wa.Manager.ClientFor(sessionID)`, wraps it with `NewWhatsmeowClient`, and
  delegates. When the session has no connected client (or no session is on the
  context) it returns `domain.ErrNotImplemented`, surfaced as the §13
  `not_implemented` (501) envelope — a send fails loudly rather than panicking on
  a nil client.

The production `WAClient` replaces the earlier `StubWAClient` placeholder; sends reach WhatsApp for
any connected session. `wa.Manager.ClientFor` type-asserts the managed session's
`waClient` to the concrete `*whatsmeow.Client`, so test sessions (which inject a
fake client) cleanly report "not available".

## Rate limiter (ratelimit.go)

`NewRedisRateLimiter(rdb, opts...)` — two fixed windows (60s, 3600s) per session,
checked and incremented in one atomic Lua round-trip. The script verifies BOTH
windows before incrementing EITHER, so a request that would breach the hour
budget does not waste a minute token (counters never drift). First INCR of a
window sets its TTL → the window self-resets on expiry. A limit `<= 0` means
"unlimited" for that window. Keys: `wa:rl:<session>:min` / `:hour`
(`WithKeyPrefix` overrides).

Chosen over a token bucket: it is exact against the masterplan's two stated
budgets, needs no background refill, and the dual-window check is a single
atomic op.

## Decisions

- **Sync over-limit = error, async = defer** (open-decision #3): sync returns
  `domain.ErrRateLimited` with `details.retryAfterSeconds`; async persists a
  `queued` row regardless of budget.
- **Idempotency is durable via the outbox**: sync sends with a key persist a
  `sending` row, then flip to `sent`/`failed` with the `wa_message_id` recorded,
  so a later replay reconstructs the original `SendResult`. A duplicate-insert
  race falls back to replaying the stored row.
- **Media is base64-inline and never retained**: `validate` requires `media.data`
  and enforces `MaxMediaBytes`; `dispatch` decodes it (a data: URI is accepted)
  and hands the bytes to `SendMedia`. The bytes ride the `outbox` payload only
  until dispatch — `store.OutboxRepo.UpdateStatus` strips `$.media.data` once the
  row is `sent` (kept on `failed` for retry). The `messages` row records only the
  caption + media metadata, never the bytes.
- **Pacing** is opt-in jitter (`WithPacing`) applied before each sync dispatch;
  the RNG is injectable for deterministic tests.

## How it's tested

Table-driven, all boundaries faked through the consumer interfaces:

- `ratelimit_test.go` (miniredis): under/over limit for per-min and per-hour,
  window reset via `FastForward`, per-session isolation, unlimited-when-zero.
- `sender_test.go` (in-memory `fakeOutbox`, recording `fakeWA`, allow/deny
  limiters, `fixedClock`):
  - type routing (text/poll/location/contact, and media → `SendMedia`) hits the
    right WAClient method; media base64 is decoded and the bytes forwarded;
  - media without `data`, or invalid base64, → `validation_error`, no dispatch;
  - validation errors for each type;
  - sync rate-limited → `rate_limited` error + no dispatch;
  - async persists a queued row, defers under a deny limiter, no inline dispatch;
  - idempotency replay (sync and async) returns the stored result without a
    second WhatsApp call / second insert;
  - sync failure records a `failed` outbox row;
  - message-op routing + validation + rate limiting;
  - pacing applied; `Dispatch` reusable.

Build/test gate: `CGO_ENABLED=0 go build|test ./internal/wa/outbound/...`.
