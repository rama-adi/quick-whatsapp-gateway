# Inbound Pipeline

Status: implemented. Package `internal/wa/inbound`.

Production wiring lives in `cmd/server`: the `wa.Manager` forwards raw
whatsmeow events into `service.InboundPipelineHandler`, backed by the real
pipeline, the gateway MySQL repos, Redis stream publisher, webhook enqueuer,
event-log appender, and manager-backed WA live ops. This is what populates
`chats` / `messages`, sends configured auto-read receipts / composing presence,
and turns non-heartbeat WhatsApp activity into NDJSON stream events.

The ordered inbound pipeline from masterplan §9. For every whatsmeow event
(tagged with its session/organization) it runs six stages, in order, with a
short-circuit on the command interceptor. It is the consumer of normalization
and the producer of all inbound side-effects (capture, persist, auto-read,
fan-out).

## Scope

1. **Normalize** — a raw whatsmeow event becomes the versioned envelope
   (`domain.Event`) plus a transport-free working view (`NormalizedMessage`).
   Raw protobufs never leave the Normalizer.
2. **Command interceptor** (§8/§9) — on the admin session, an inbound,
   non-echo text whose body starts with `WHATSAPP_ADMIN_CMD_PREFIX` is handed to
   the `CommandRegistry` and **dropped**: not persisted, not emitted. v2 ships the
   interceptor + a no-op registry (`amlogin` is later).
3. **Identity capture** (§9) — upsert the central `whatsapp_identities` row (push
   name preferred), keyed by the **canonical non-AD LID** (`:device` stripped).
   When a push name arrives **without** a canonical LID (a `contact.update` /
   push-name event carries only a JID), it is used opportunistically to **fill a
   nameless existing identity**, matched by `lid` or `phone_jid` — never inserting
   (a merely-synced contact shouldn't create an identity) and never clobbering a
   name we already have. There is no per-session contacts table — DM "found"
   status is derived later from `chats`. For groups upsert `whatsapp_groups` + the
   `whatsapp_group_members` pivot (per-group `tag` + role, role defaults to
   `member`).
4. **Persist** (§9) — upsert `chats`; insert `messages` (incl. `raw_json`); a poll
   creation also upserts `polls` (its options, so later votes can be resolved);
   `edit`/`revoke` flip flags on the target; receipts update `status`/`ack_level`;
   poll updates insert `poll_votes` idempotently (with the resolved
   `selected_options`; replay of the same poll-update event is ignored by the
   store). **Content-less system messages are dropped
   here** — the classifier's unrecognized-content fallthrough (`MsgType=="system"`:
   E2E-encryption notices, ephemeral settings, sender-key distribution, …) carries
   no displayable body, so after identity capture it is dropped (not persisted, not
   fanned out). Real WhatsApp renders group notices from typed group events, not
   from these.
5. **Auto-read** (§9) — if the session has `auto_read`, send a read receipt
   **before** any fan-out; optional `presence_typing` → "composing". The receipt's
   sender is `SenderJID`, falling back to `SenderLID` when the phone JID is unknown
   (LID-only senders) — WA routes a group receipt by participant and drops one with
   an empty sender. Best-effort: WA-client errors are logged, never fatal.
6. **Fan-out** (§9) — append `event_log` (durable resume cursor) + publish to
   the event sink + enqueue webhook deliveries. The three sinks are independent;
   failures are joined and returned.

## Key types

- `Pipeline` — the orchestrator. Constructor injection of all collaborators; no
  globals. Stateless and safe to share across sessions. `Process(ctx, sessionID,
  organizationID, isAdminSession, evt any) error` runs all stages.
- `NormalizedMessage` — the decoupled working view (in `types.go`). Owned by
  this package, NOT `internal/wa/events`, per "interfaces defined by the
  consumer". `Kind` (`message`/`receipt`/`poll_vote`/`edit`/`revoke`/`other`)
  selects the capture/persist path. Carries sender/identity, message body,
  optional `Group`+`Members`, `Poll` (poll-creation options), `Receipt`,
  `PollVote`, and `RawJSON`.
- `NoopCommandRegistry` — v2 registry, recognizes nothing.
- `SystemClock` — production `Clock` backed by `domain.NowMs`.

### Consumer interfaces (ports.go)

The package imports only stdlib + `internal/domain`. Every collaborator is a
small consumer interface that Phase 3 satisfies with concrete types:

- `Normalizer.Normalize(ctx, evt any, sessionID, organizationID string) (domain.Event, *NormalizedMessage, bool)`
  — the bool is "ok"; false drops the event silently. It takes a `ctx` because a
  poll vote is decrypted + resolved during normalization (gated on the event kind);
  the composition-layer `InboundNormalizer` holds the whatsmeow client and the
  `polls` reader needed for that.
- `CommandRegistry.Handle(ctx, sessionID, body string) (handled bool, err error)`.
- `EventSink.Publish(ctx, domain.Event) error`.
- `WebhookEnqueuer.Enqueue(ctx, domain.Event) error`.
- `WAClient.SendReadReceipt(ctx, sessionID, chatJID, senderJID string, messageIDs []string) error`
  and `SendPresence(ctx, sessionID, chatJID, state string) error`.
- `Clock.NowMs() int64`.
- `Repos` — the subset of store upserts/inserts used by capture/persist/fan-out
  (`UpsertIdentity`, `FillIdentityName`, `UpsertGroup/GroupMember`, `UpsertChat`,
  `InsertMessage`, `MarkMessageEdited/Deleted`, `UpdateMessageStatus`,
  `UpsertPoll`, `InsertPollVote`, `AppendEventLog`). Arguments are decoupled `*Upsert`/`*Insert`/
  `*Fill` structs so the store's row types are not a dependency.

Options: `WithCommandPrefix(string)` (default `"am"`; empty disables the
interceptor), `WithSessionConfig(SessionConfigFunc)` (resolves per-session
`auto_read`/`presence_typing`; absent ⇒ auto-read disabled), `WithLogger`.

## Decisions

- **Ordering is the contract.** Capture → persist → auto-read → fan-out. The
  read receipt is sent strictly before any event is fanned out, so a consumer
  that replies on the fanned event never races the receipt and leaves the chat
  stuck-unread (§9). A dedicated test asserts `SendReadReceipt` precedes
  `Publish`/`AppendEventLog`/`Enqueue`, and `InsertMessage` precedes the receipt.
- **Interceptor is narrow.** Only `isAdminSession` + `KindMessage` + non-echo +
  non-empty body with the prefix is intercepted. Echoes, receipts, poll votes
  and non-text events on the admin number flow normally, so the admin number
  does double duty as a regular API number (§8). A prefixed admin message is
  dropped even when the no-op registry returns `handled=false`, and even when the
  registry errors (the error is logged, the drop stands).
- **Identity capture runs for any kind that carries a sender** (push name
  preferred, COALESCE'd so a later nameless sighting never wipes a known name).
  A push name seen **without** a canonical LID (contact.update / push-name events)
  still gets used — it fills a nameless existing identity by `lid`/`phone_jid`
  (`FillIdentityName`), never inserting. There is no message-count / DM bookkeeping
  in the pipeline — DM "found" status is derived from `chats` at read time.
- **Capture/persist errors abort and return** (wrapped `inbound capture`/`inbound
  persist`) so the caller can retry; re-processing is idempotent
  (`UNIQUE(session_id, wa_message_id)`). Auto-read errors are non-fatal.
  Fan-out attempts all three sinks and `errors.Join`s failures, preferring the
  durable `event_log` row to survive even when live delivery fails.
- **Decoupling.** `NormalizedMessage` is defined here, not in `events`. The
  Normalizer (Phase 3) maps whatsmeow protobufs onto it; the pipeline operates
  purely on this struct + `domain.Event`.

## How it's tested

Table-driven tests (`pipeline_test.go`) with hand-written fakes for every
interface (`fakes_test.go`), including a shared `callOrder` recorder for ordering
assertions. Coverage ~84%, `-race` clean. Cases:

- DM vs group capture (identity captured for both; group + members upserted for
  groups; empty role defaults to `member`).
- Interceptor matrix: admin+prefix drop; admin no-prefix / non-admin / echo /
  empty-prefix / receipt all processed; registry error still drops.
- Auto-read ordering (receipt before all fan-out; persist before receipt) and the
  off paths (flag off, no resolver, unknown session, echo, non-message kind).
- Receipt → `UpdateMessageStatus` (no message insert, no count bump, no
  auto-read); poll creation → `UpsertPoll` (+ message insert); poll update →
  `InsertPollVote` with a non-empty normalized voter key (canonical LID preferred,
  otherwise sender phone JID); edit/revoke flag flips.
- Normalize-drop runs no stages; from-me echo persists as `out`; sender-less
  events skip identity/contact but still fan out; capture-error aborts;
  fan-out errors joined while `event_log` still appended; auto-read error
  non-fatal.
