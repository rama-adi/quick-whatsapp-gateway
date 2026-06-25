# Eventing

The eventing subsystem has two halves that share one envelope:

1. **Normalization** (`internal/wa/events`) — translate raw whatsmeow events into the
   versioned domain event catalog (§9), classify chats, and apply source-level ignore
   rules. Documented here.
2. **Transport** (`internal/stream`, `internal/webhooks`) — the NDJSON stream and the
   webhook dispatcher that carry the envelope. Documented in their own specs and the
   transport section below (owned by another subsystem).

The shared envelope is `domain.Event` (schema `v1`): `{schema, id (evt_<ulid>), event,
session, tenant, timestamp (epoch-ms), payload}` — see `internal/domain/event.go`.

---

## Normalization (`internal/wa/events`)

### Scope

Pure, dependency-light translation. The package imports only the Go stdlib, whatsmeow
(`types`, `types/events`, `proto/waE2E`, `proto/waCommon`) and `internal/domain`. It holds
no IO and no live client, so it is trivially unit-testable and parallel-safe. Collaborators
(config) are consumer-defined here (`IgnoreConfig`); Phase 3 wires the real config in.

### Key entry point

```go
func Normalize(evt any, sessionID, tenantID string) (domain.Event, PersistResult, bool)
```

- Switches over the handled whatsmeow event types and returns:
  - a `domain.Event` envelope whose `Payload` is a **wire-safe, camelCase struct** — never
    a raw protobuf;
  - a `PersistResult`, the structured, protobuf-free handoff the inbound pipeline consumes
    (capture → persist → fan-out, §7), so downstream stages never re-parse the raw event;
  - `ok=false` for events the catalog does not represent (the caller drops them).

### Event mapping (§9 catalog)

| whatsmeow event | catalog `event` | `PersistKind` |
|---|---|---|
| `*events.Message` (text/media/location/contact/poll) | `message` / `message.from_me` | `PersistMessage` |
| `*events.Message` reaction (`GetReactionMessage`) | `message.reaction` | `PersistMessageReaction` |
| `*events.Message` edit (`IsEdit` / `ProtocolMessage{MESSAGE_EDIT}`) | `message.edited` | `PersistMessageEdit` |
| `*events.Message` revoke (`ProtocolMessage{REVOKE}`) | `message.revoked` | `PersistMessageRevoke` |
| `*events.Message` poll vote (`GetPollUpdateMessage`) | `poll.vote` | `PersistPollVote` |
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
   here — decryption happens later via `cli.DecryptPollVote`).
5. `GetPollCreationMessage()` → poll (name/options/selectableCount).
6. `GetLocationMessage()` → location (lat/long/name/address).
7. `GetContactMessage()` → contact (displayName + vCard verbatim).
8. Any media message (image/video/audio/document/sticker) → media **metadata only**.
9. `Conversation` or `ExtendedTextMessage` → text.
10. otherwise → `system` / `SubtypeUnknown` (still emitted as a `message`).

### Extracted fields (`NormalizedMessage`)

Maps ~1:1 onto the `messages` table plus identity/contacts capture inputs:
chat JID + `ChatClass`, sender JID, sender LID (only when `SenderAlt` is on the `lid`
server), `FromMe`, push name, epoch-ms timestamp, body/caption, quoted stanza id, mentioned
JIDs, media flag + `MediaMeta` (mimetype/size/filename), reaction/edit/revoke target id,
and structured `Location`/`Contact`/`Poll` bodies.

### Media policy (§9)

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
- Poll **votes** are emitted with the target poll id only; option decryption is deferred.

### How it's tested

Table-driven unit tests synthesize `*events.Message` values for each sub-type (text, text
from-me, group text, quoted+mentions, reaction, edit, revoke, location, contact, poll,
poll-vote, image/document media) and assert the mapped catalog `event`, `PersistKind`,
sub-type, persisted `type` string, and extracted fields (incl. the §9 media-null
invariant). Separate tables cover receipts→status, session-status events, QR/pair, presence
+ chat-presence, group update vs participant, push-name/contact, call offer, newsletter, the
sender-LID-vs-PN rule, and the unknown-event drop. Ignore rules and `ClassifyChat` have
their own JID-classification tables. ~85% statement coverage; `go test` and `go vet` clean.

---

## Transport (NDJSON stream + webhooks) — owned by another subsystem

Status: see `internal/stream` and `internal/webhooks` specs. Both carry the same
`domain.Event` envelope produced here; the fan-out stage (§7) appends to `event_log`,
publishes to Redis pub/sub for stream subscribers, and enqueues webhook deliveries.
