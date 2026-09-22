# Eventing

The eventing subsystem has two halves that share one envelope:

1. **Normalization** (`backend/internal/wa/events`) — translate raw whatsmeow events into the
   versioned domain event catalog (§11), classify chats, and apply source-level ignore
   rules. Documented here.
2. **Transport** (`backend/internal/stream`, `backend/internal/webhooks`) — the realtime WebSocket and the
   webhook dispatcher that carry the envelope. Documented in their own specs and the
   transport section below (owned by another subsystem).

The shared envelope is `domain.Event` (schema `v1`): `{schema, id (evt_<ulid>), event,
session, organization, timestamp (epoch-ms), payload}` — see `backend/internal/domain/event.go`.
The envelope carries the **owning organization** (`organization`, a better-auth org id)
so every transport can scope delivery by org. The catalog includes interactive selections as `message.interactive_reply`.

**Auth (per §4).** Realtime ticket minting authenticates with the API's two-acceptor
middleware — a JWKS-verified better-auth **JWT** (human) or a better-auth **api-key**
(machine). There is no event-specific auth path; the resolved org scopes what a caller
sees. Outbound webhooks use their configured HMAC signing secret.

---

## Normalization (`backend/internal/wa/events`)

### Scope

Pure, dependency-light translation. The package imports only the Go stdlib, whatsmeow
(`types`, `types/events`, `backend/proto/waE2E`, `backend/proto/waCommon`) and `backend/internal/domain`. It holds
no IO and no live client, so it is trivially unit-testable and parallel-safe. Collaborators
(config) are consumer-defined here (`IgnoreConfig`); Phase 3 wires the real config in.

### Key entry point

```go
func Normalize(evt any, sessionID, organizationID string) (domain.Event, PersistResult, bool)
```

- Switches over the handled whatsmeow event types and returns:
  - a `domain.Event` envelope whose `Payload` is a **wire-safe, camelCase struct** — never
    a raw protobuf;
  - a `PersistResult`, the structured, protobuf-free handoff the inbound pipeline consumes
    (capture → persist → fan-out, §9), so downstream stages never re-parse the raw event;
  - `ok=false` for events the catalog does not represent (the caller drops them).

### Event mapping (§11 catalog)

| whatsmeow event | catalog `event` | `PersistKind` |
|---|---|---|
| `*events.Message` (text/media/location/contact/poll) | `message` / `message.from_me` | `PersistMessage` |
| `*events.Message` reaction (`GetReactionMessage`) | `message.reaction` | `PersistMessageReaction` |
| `*events.Message` edit (`IsEdit` / `ProtocolMessage{MESSAGE_EDIT}`) | `message.edited` | `PersistMessageEdit` |
| `*events.Message` revoke (`ProtocolMessage{REVOKE}`) | `message.revoked` | `PersistMessageRevoke` |
| `*events.Message` poll vote (`GetPollUpdateMessage`) | `poll.vote` | `PersistPollVote` |
| `*events.Message` native-flow / button / list selection | `message.interactive_reply` | `PersistMessage` |
| timed poll close (`PollRecapWorker`) | `poll.recap` | synthetic / no inbound persist |
| `*events.Receipt` (delivered/read/played) | `message.status` | `PersistMessageStatus` |
| `*events.Connected` | `session.status` (working) | `PersistSessionStatus` |
| `*events.Disconnected` | `session.status` (starting) | `PersistSessionStatus` |
| `*events.LoggedOut` | `session.status` (logged_out) | `PersistSessionStatus` |
| `*events.StreamReplaced` | `session.status` (failed) | `PersistSessionStatus` |
| `*events.QR` | `auth.qr` | `PersistNone` |
| `*events.PairSuccess` | `auth.code` | `PersistNone` |
| `*events.Presence` / `*events.ChatPresence` | `presence.update` | `PersistNone` |
| `*events.GroupInfo` (metadata) | `group.update` | `PersistGroupUpdate` |
| `*events.GroupInfo` (join/leave/promote/demote) / `*events.JoinedGroup` | `group.participant` / `group.update` | `PersistGroupParticipant` / `PersistGroupUpdate` |
| `*events.Picture` | `chat.update` | `PersistNone` |
| `*events.Contact` / `*events.PushName` | `contact.update` | `PersistContactUpdate` |
| `*events.CallOffer` | `call.incoming` | `PersistNone` |
| `*events.Newsletter{Join,Leave,MuteChange}` | `newsletter.update` | `PersistNone` |

Anything else → `ok=false`.

### Message sub-type detection

`normalizeMessage` reads `e.Message` (the lib already unwraps
Ephemeral/ViewOnce/DeviceSent/Edited). Detection order (control messages first):

1. `GetReactionMessage()` → reaction (emoji + target id from the `MessageKey`).
2. `GetProtocolMessage()` type `REVOKE` → revoke (target id).
3. `e.IsEdit` or `ProtocolMessage` type `MESSAGE_EDIT` → edit (new body from
   `GetEditedMessage()`, target id from the key). Note: the real constant is
   `ProtocolMessage_MESSAGE_EDIT` (value 14), not the `EDIT` the recon cheat-sheet implied.
4. `GetPollUpdateMessage()` → poll vote (target poll id; the vote payload stays encrypted
   here — decryption + option-text resolution happen in the composition-layer normalizer,
   which holds the whatsmeow client and the stored poll options, see below).
5. `GetPollCreationMessage()` **or** `GetPollCreationMessageV2()`/`V3()` → poll
   (name/options/selectableCount/endTime/hideVotes). Current WhatsApp clients send the V3
   field; checking only the original (pre-V2) field was why modern polls were misclassified as a
   content-less "system" message and dropped. (V4+ wrap the poll in a `FutureProofMessage`
   and are not handled.)
6. `GetLocationMessage()` → location (lat/long/name/address).
7. `GetContactMessage()` → contact (displayName + vCard verbatim).
8. Any media message (image/video/audio/document/sticker) → media **metadata only**.
9. `Conversation` or `ExtendedTextMessage` → text.
10. otherwise → `system` / `SubtypeUnknown` (still emitted as a `message`).

### Extracted fields (`NormalizedMessage`)

Maps ~1:1 onto the `messages` table plus identity/contacts capture inputs:
chat JID + `ChatClass`, sender JID, sender LID (only when `SenderAlt` is on the `lid`
server), `FromMe`, push name, epoch-ms timestamp, body/caption, quoted stanza id,
mentioned JIDs (stored internally as a list, emitted on message events as a
JID-keyed map containing `pushName` and per-group member `tag` when known), media
flag + `MediaMeta` (mimetype/size/filename), reaction/edit/revoke target id, and
structured `Location`/`Contact`/`Poll` bodies.

**Quoted-message context (replies).** A reply's `ContextInfo` carries the quoted
message inline, so message events expose the quote without a REST round-trip:

- `quotedSenderJid` / `quotedSenderLid` — the quoted message's author, from
  `ContextInfo.Participant` (canonicalized to non-AD; routed to the JID or LID
  field by server). **Guaranteed** from the protocol frame for a genuine reply;
  back-filled from the locally stored quoted message when the frame omits it.
- `quotedBody` — the quoted message's text/caption, from
  `ContextInfo.QuotedMessage`, truncated to 4096 bytes on a UTF-8 boundary.
  **Guaranteed** from the frame for text/caption replies; else the stored body.
- `quotedFromMe` — true when the quoted message was sent by this account.
  **Best-effort**: resolved authoritatively from the locally stored quoted
  message (the protocol frame has no reliable "quoted was mine" flag), so it is
  `false` when the quoted message predates local retention. The quote-enrichment
  stage runs in the inbound pipeline (after mention enrichment, before persist);
  the resolved fields are mirrored into the persisted `raw_json`.

All quoted-* fields are `omitempty` and set only when `quotedMessageId` is present.

### Media policy (§11)

Media is **never downloaded** in v1. For media messages `HasMedia=true` and `MediaInfo`
carries the metadata for the `messages.media_meta` column, but the **wire** `payload.media`
field is **always null** — `messagePayload` deliberately sets `Media: nil`.

### Ignore rules

`IgnoreRules` (built from the four `IGNORE_*` config bools via `IgnoreConfig`) classifies a
chat purely by its JID server, so it works on a bare string with no live client:

- `status@broadcast` → status (`IgnoreStatus`)
- `g.us` → group (`IgnoreGroups`)
- `newsletter` → channel (`IgnoreChannels`)
- `broadcast` (excluding the status JID) → broadcast list (`IgnoreBroadcast`)
- anything else → DM (never ignored)

`status@broadcast` is classified as **status**, not broadcast, so the two flags are
independent. An unparseable JID **fails open** (not ignored) — silently dropping
unclassifiable data is worse than persisting an odd JID downstream can still record.

`ClassifyChat` is exported and reused by the persistence layer to set `chats.type`.

### Decisions

- **No raw protobufs cross the package boundary.** Every nested proto is flattened into
  plain Go fields in the payload structs and `NormalizedMessage`.
- **`PersistResult` is the pipeline contract**, not the wire payload. It tags a
  `PersistKind` so the inbound pipeline dispatches without re-inspecting the event, and
  carries the parsed `*NormalizedMessage` for message-bearing kinds.
- **`Disconnected` is non-terminal** (manager reconnects) → reported as `starting`;
  `LoggedOut`/`StreamReplaced` are terminal → `logged_out`/`failed`.
- **Non-lifecycle receipts** (sender/retry/server-error/etc.) → `ok=false`.
- Poll **votes**: `events.Normalize` produces the target poll id only (the vote is still
  encrypted). The composition-layer `InboundNormalizer` then decrypts it via the live
  whatsmeow client (`cli.DecryptPollVote` → SHA-256 option hashes) and resolves those
  hashes to option text against the `polls` table (populated when the poll creation was
  persisted), writing `selectedOptions` onto both the `poll.vote` envelope and the
  `poll_votes` row. Poll vote persistence uses the normalized sender identity as the voter
  key: canonical LID when available, otherwise the sender phone JID. The key must be
  non-empty for a real vote so distinct voters do not collapse in replay de-dupe or recaps.
  An unresolvable hash (poll never seen) falls back to the raw hash.
- Poll **recaps**: timed poll creation persists `polls.end_time` and schedules the close
  timestamp into a Redis sorted set for near-realtime wakeups. `PollRecapWorker` still uses
  MySQL as the source of truth: it sweeps due rows, claims `recap_emitted_at`, aggregates the
  latest vote per non-empty voter key from `poll_votes`, and emits one synthetic
  `poll.recap` event with option counts, total voters, `selectableCount`, `endTime`, and
  `hideVotes`. When `hideVotes=false`, the payload also includes a deterministic
  `voters` array sorted by stored voter key; each entry carries the voter key, the display
  name resolved at read time from `whatsapp_identities`, and the latest selected options.
  When `hideVotes=true`, `voters` is omitted entirely so the synthetic recap does not leak
  identities. Historical rows with an empty voter key are counted defensively as distinct
  rows rather than being merged under the empty string.

### How it's tested

Table-driven unit tests synthesize `*events.Message` values for each sub-type (text, text
from-me, group text, quoted+mentions, reaction, edit, revoke, location, contact, poll,
poll-vote, image/document media) and assert the mapped catalog `event`, `PersistKind`,
sub-type, persisted `type` string, and extracted fields (incl. the §11 media-null
invariant). Separate tables cover receipts→status, session-status events, QR/pair, presence
+ chat-presence, group update vs participant, push-name/contact, call offer, newsletter, the
sender-LID-vs-PN rule, and the unknown-event drop. Ignore rules and `ClassifyChat` have
their own JID-classification tables. ~85% statement coverage; `go test` and `go vet` clean.

---

## Transport (realtime + webhooks) — owned by another subsystem

Status: see `backend/internal/stream` and `backend/internal/webhooks` specs. Both carry the same
`domain.Event` envelope produced here. Since Increment 9 the **only** gateway-side
transport is the event journal: the gateway appends each envelope to its local
journal (no `event_log`, no Redis publication, no webhook enqueue — those
dependencies no longer exist on the gateway).

API-owned ingestion hands an envelope to
`application.CommittedEventConsumer` only **after** its event-log transaction
commits. `application.CommittedEventWorkStore` is the durable work boundary: its
store implementation claims only committed, incomplete envelopes and records
completion only after every consumer accepts the event. `service.CommittedEventWorker`
uses that port, and its stateless `CommittedEventDispatcher` runs registered
projections (the API-side WhatsApp-data projections), then realtime Redis
publication, then webhook enqueue in that order. Failed attempts remain
incomplete for the store's retry/lease path; durable consumers must still use
`event.id` as their idempotency key because a crash can occur after a consumer
accepts an event and before completion is recorded.

Lease eligibility uses the claim's current time, never its future expiration.
A session's later events cannot overtake a currently leased predecessor. Within
a claimed batch, failure holds the remaining events for that session but does
not block unrelated sessions. Worker failures are logged with the event ID and
remain incomplete for retry; retention must not remove their event-log payloads.


### Increment 5/9 durable handoff

Private-control gateways require an absolute `GATEWAY_JOURNAL_PATH` for a separate SQLite journal.
The journal accepts the normalized `domain.Event` JSON only while the current desired-state assignment
owns its organization/session, preserving that assignment epoch with the entry. It replays entries in
committed sequence order as protobuf `Struct` payloads over the control stream; exactly one batch is
in flight and the API acknowledgement must equal that batch's last journal sequence before deletion.
The API decodes each protobuf envelope, validates its identity against the control-frame metadata,
and stores only its inner type-specific JSON payload in `event_log.payload`. Committed-event
consumers reconstruct the envelope from the row columns; protobuf bytes or a nested envelope must
never enter that JSON column.
Each connection waits for a fresh, successfully applied complete desired-state
snapshot before replaying. An entry belonging to an assignment omitted from that
snapshot, or carrying a different assignment epoch/organization, is retired:
the API no longer authorizes that work. STOP assignments retain ownership and
keep their events. Same-assignment events survive reconnects with a fresh
connection epoch. A matching API ACK advances mixed batches; an entirely retired
prefix can advance locally under snapshot authority. Transient transport errors
never authorize deletion. Malformed journal entries remain visible errors and
are preserved for operator repair rather than silently discarded.

Reconnects then replay the oldest remaining unacknowledged batch. A critical journal capacity state makes the gateway
unready, while a failed append backpressures the producing pipeline. Every heartbeat carries optional
journal-pressure telemetry (`journal_state`/`journal_entries`/`journal_bytes`, §7): an unreadable
journal omits the report instead of fabricating one, and the API persists the last reported pressure
on the registry row for admin observability. Paused/critical states are advisory to optional sync
work; no gateway sync work is pausable yet, so the seam is reserved, not exercised.

In control mode (the only mode since Increment 9) the gateway does
not append `event_log`, publish Redis events, or enqueue webhooks: API ingestion owns the single
event-log transaction and post-commit fan-out.
Poll-recap emission is API-owned in every mode: the durable MySQL sweep and its event append,
realtime publish, and webhook enqueue run beside the committed-event worker on the API; the Redis
sorted set is only a low-latency wake-up index and gateways no longer write it.

## Interactive selections

`message.interactive_reply` uses the message envelope with
`payload.interactiveReply: {kind: "button"|"list", id, title?}` and
`type: "interactive_reply"`. `quotedMessageId` identifies the original message
when supplied by WhatsApp. `chatJid`, `senderJid`/`senderLid`, and `fromMe` retain
normal message semantics, including group participants and linked-device echoes.
The event replaces the generic `message`/`message.from_me` event for that selection;
subscribe to it explicitly or use `*`. It travels through the existing webhook
and realtime transports and persists as a message with its normalized raw JSON.

Normalization accepts native-flow `quick_reply`/`single_select` responses and
legacy `ButtonsResponseMessage`/`ListResponseMessage`. Malformed JSON, missing IDs,
and unknown native flows remain unknown messages rather than fabricated selections.
IDs and display text are untrusted sender input. Applications correlate session,
chat, original message, and sender, then validate the ID against their own choices.
Typed numeric replies remain ordinary text messages; no selection state is held
by the gateway.
