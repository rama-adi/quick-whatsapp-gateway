# Inbound Pipeline

Status: implemented. Package `internal/wa/inbound`.

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
   the `CommandRegistry` and **dropped**: not persisted, not emitted, not counted
   as a contact. v2 ships the interceptor + a no-op registry (`amlogin` is later).
3. **Identity / contacts capture** (§9) — upsert global `whatsapp_identities`
   (push name preferred); upsert per-account `whatsapp_contacts` (DMs set
   `seen_in_dm` + DM timestamps; `message_count` bumps only for real inbound,
   non-echo messages); for groups upsert `whatsapp_groups` +
   `whatsapp_group_members` (nickname + role, role defaults to `member`).
4. **Persist** (§9) — upsert `chats`; insert `messages` (incl. `raw_json`);
   `edit`/`revoke` flip flags on the target; receipts update `status`/`ack_level`;
   poll updates insert `poll_votes`.
5. **Auto-read** (§9) — if the session has `auto_read`, send a read receipt
   **before** any fan-out; optional `presence_typing` → "composing". Best-effort:
   WA-client errors are logged, never fatal.
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
  optional `Group`+`Members`, `Receipt`, `PollVote`, and `RawJSON`.
- `NoopCommandRegistry` — v2 registry, recognizes nothing.
- `SystemClock` — production `Clock` backed by `domain.NowMs`.

### Consumer interfaces (ports.go)

The package imports only stdlib + `internal/domain`. Every collaborator is a
small consumer interface that Phase 3 satisfies with concrete types:

- `Normalizer.Normalize(evt any, sessionID, organizationID string) (domain.Event, *NormalizedMessage, bool)`
  — the bool is "ok"; false drops the event silently.
- `CommandRegistry.Handle(ctx, sessionID, body string) (handled bool, err error)`.
- `EventSink.Publish(ctx, domain.Event) error`.
- `WebhookEnqueuer.Enqueue(ctx, domain.Event) error`.
- `WAClient.SendReadReceipt(ctx, sessionID, chatJID, senderJID string, messageIDs []string) error`
  and `SendPresence(ctx, sessionID, chatJID, state string) error`.
- `Clock.NowMs() int64`.
- `Repos` — the subset of store upserts/inserts used by capture/persist/fan-out
  (`UpsertIdentity/Contact/Group/GroupMember/Chat`, `InsertMessage`,
  `MarkMessageEdited/Deleted`, `UpdateMessageStatus`, `InsertPollVote`,
  `AppendEventLog`). Arguments are decoupled `*Upsert`/`*Insert` structs so the
  store's row types are not a dependency.

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
- **`message_count` bumps only for `KindMessage` and not `FromMe`.** Receipts,
  poll votes, edits/revokes, and own-number echoes refresh identity/contact
  `last_seen` but never count the peer as having messaged us.
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

- DM vs group capture (seen_in_dm only for DMs; group + members upserted for
  groups; empty role defaults to `member`).
- Interceptor matrix: admin+prefix drop; admin no-prefix / non-admin / echo /
  empty-prefix / receipt all processed; registry error still drops.
- Auto-read ordering (receipt before all fan-out; persist before receipt) and the
  off paths (flag off, no resolver, unknown session, echo, non-message kind).
- Receipt → `UpdateMessageStatus` (no message insert, no count bump, no
  auto-read); poll update → `InsertPollVote`; edit/revoke flag flips.
- Normalize-drop runs no stages; from-me echo persists as `out`; sender-less
  events skip identity/contact but still fan out; capture-error aborts;
  fan-out errors joined while `event_log` still appended; auto-read error
  non-fatal.
