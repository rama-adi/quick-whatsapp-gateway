# Resource handlers (chats · groups · channels · status · presence · admin)

Status: implemented (Phase 3, stage "resource handlers").

Covers the §11 resource groups beyond sessions/messages/keys/webhooks. Thin
handlers (`internal/http/handlers/{chat,contact,group,resources}.go`) validate +
decode, call a service, and encode; services (`internal/service/{chat,contact,group,misc_resources}.go`)
hold the logic; repos hold SQL. Every service method verifies the session belongs
to the caller's tenant first (foreign tenant => `not_found`).

## Live-ops boundary (the key design point)

Operations that must hit a connected `*whatsmeow.Client` (group management,
on-WhatsApp checks, picture/about/block, presence, channels, status) are
delegated to narrow **ports defined in the service package**
(`internal/service/liveops.go`): `GroupOps`, `ContactDirectory`,
`PresenceController`, `ChannelOps`, `StatusPoster`. The exchanged value types live
in `internal/domain/liveops.go` (`GroupInfo`, `GroupSettings`, `OnWhatsApp`,
`ProfilePicture`, `GroupParticipantAction`) so both the ports and the adapter use
identical types without an import cycle.

The production adapter is `wa.LiveOps` (`internal/wa/liveops.go`), a
manager-backed value returned by `Manager.LiveOps()`. It resolves the per-session
live client via `Manager.Get(id)` → `ManagedSession.client` (type-asserted to a
narrow `liveClient` interface that the real `*whatsmeow.Client` satisfies) and
translates between the string-JID API surface and whatsmeow's typed calls
(recon §8/§9). When a session has no connected client it returns
`not_implemented` — consistent with the v1 outbound Sender, which is itself wired
with a stub client until the per-session client is plumbed end-to-end.

`service.New` wires one `*wa.LiveOps` into every resource service; a nil Manager
yields nil ports and the services fall back to the `not_implemented` envelope.

## Chats (§11)

| Method | Path | Backed by |
|---|---|---|
| GET | `/chats` · `/chats/{cid}` · `/chats/{cid}/messages` | store (cursor pages) |
| POST | `/chats/{cid}/read` | store — zeroes `unread_count` (local read state; per-message WA receipts are out of scope for v1) |
| PATCH | `/chats/{cid}` | store — `archived`/`pinned`/`mutedUntil`/`unmute` (nil = unchanged) |
| DELETE | `/chats/{cid}` | store |
| PUT | `/chats/{cid}/presence` | live `PresenceController.SetChatPresence` — `state` ∈ {composing,paused,recording} |

## Groups (§11)

Reads (`list`/`get`/`members`) are store-backed; `list` joins through the
per-session `whatsapp_group_members` pivot (`GroupRepo.ListBySession`) because
`whatsapp_groups` is global metadata. Mutations + invite links go through
`GroupOps`:

- create / get-info / update-participants(add,remove,promote,demote) /
  update-settings(subject,description,announce,locked) / invite get+revoke(reset)
  / join(invite code) / leave.
- `members:approve` (pending-request approval) is **not implemented** in v1 —
  not part of the narrow live client surface — and returns `not_implemented`
  consistently with the media types.

## Channels (§11)

`ChannelOps` (create / follow / unfollow / mute). whatsmeow's newsletter API is
not part of the v1 live client, so all four return `not_implemented`.
`GET /channels/{jid}/messages` reads stored messages by `chat_jid`.

## Status (§11)

`POST /status` discriminates on `type`. `text` posts via `StatusPoster.PostText`
(not_implemented in v1 — uses the stubbed Sender path). `image` (and any other
media) returns `501 not_implemented`, matching the media send types.

## Presence (§11)

`PUT /presence` — `state` ∈ {online,offline} via `PresenceController.SetPresence`.

## Admin (§11)

`GET /api/v1/admin/sessions` — cross-tenant oversight via
`AdminService.ListAllSessions` (`SessionRepo.ListAll`). Gated at the router by
`RequireManage` (the highest API-key permission). True `super_admin` role
enforcement lives on the Authula dashboard/cookie path; tenant/user management is
the Authula `/auth/admin/*` surface (already mounted) — not re-implemented here.

## Media (cross-cutting)

Media send types (image/video/audio/document/sticker) and image status return
`501 not_implemented` consistently. Domain helper: `domain.ErrNotImplemented`.

## Tests

- Handlers: `internal/http/handlers/resources_test.go` — httptest + fake services
  per group: happy path, validation propagation, auth (401) failures, and the 501
  media/not-implemented cases.
- Services: `internal/service/resources_test.go` — sqlmock-backed store + fake
  live ports: tenant-ownership rejection, validation, live delegation, and the
  nil-port / image-status `not_implemented` fallbacks.
- Store: `GroupRepo.ListBySession` covered in `internal/store/repos_more_test.go`.
