# Resource handlers (chats · groups · channels · status · presence · admin)

Status: implemented (Phase 3, stage "resource handlers").

Covers the §13 resource groups beyond sessions/messages/webhooks (api-key management
lives in the frontend's better-auth api-key plugin — the gateway has no `/keys`
routes). Thin handlers (`internal/http/handlers/{chat,contact,group,resources}.go`)
validate + decode, call a service, and encode; services
(`internal/service/{chat,contact,group,misc_resources}.go`) hold the logic; repos hold
SQL. Resources are **org-owned**: every service method verifies the session belongs to
the caller's **active organization** first (foreign org => `not_found`). The caller's
org comes from the §4 principal — the active org on a JWT, or the key's org on an
api-key. A platform `super_admin` (JWT `role`) crosses orgs for oversight.

**Session responses expose `gatewayId`** (§13): the gateway each session lives on —
along with the gateway's `label`/`status`/`baseUrl` from the `gateways` registry — so
the dashboard can show **where** a session runs once there is more than one gateway
(§4.5). Session pinning itself is handled by the session manager (session-manager.md).

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
`not_implemented` — consistent with the outbound Sender's session-routing client
for a session without a live connection (outbound-pipeline.md).

`service.New` wires one `*wa.LiveOps` into every resource service; a nil Manager
yields nil ports and the services fall back to the `not_implemented` envelope.

## Chats (§13)

| Method | Path | Backed by |
|---|---|---|
| GET | `/chats` · `/chats/{cid}` · `/chats/{cid}/messages` | store (cursor pages) |
| POST | `/chats/{cid}/read` | store — zeroes `unread_count` (local read state; per-message WA receipts are out of scope for v2) |
| PATCH | `/chats/{cid}` | store — `archived`/`pinned`/`mutedUntil`/`unmute` (nil = unchanged) |
| DELETE | `/chats/{cid}` | store |
| PUT | `/chats/{cid}/presence` | live `PresenceController.SetChatPresence` — `state` ∈ {composing,paused,recording} |

## Groups (§13)

Reads (`list`/`get`/`members`) are store-backed; `list` joins through the
per-session `whatsapp_group_members` pivot (`GroupRepo.ListBySession`) because
`whatsapp_groups` is global metadata. Mutations + invite links go through
`GroupOps`:

- create / get-info / update-participants(add,remove,promote,demote) /
  update-settings(subject,description,announce,locked) / invite get+revoke(reset)
  / join(invite code) / leave.
- `members:approve` (pending-request approval) is **not implemented** in v2 —
  not part of the narrow live client surface — and returns `not_implemented`
  consistently with the media types.

## Channels (§13)

`ChannelOps` (create / follow / unfollow / mute). whatsmeow's newsletter API is
not part of the v2 live client, so all four return `not_implemented`.
`GET /channels/{jid}/messages` reads stored messages by `chat_jid`.

## Status (§13)

`POST /status` discriminates on `type`. `text` posts via `StatusPoster.PostText`
(not_implemented in v2 — uses the stubbed Sender path). `image` (and any other
media) returns `501 not_implemented`, matching the media send types.

## Presence (§13)

`PUT /presence` — `state` ∈ {online,offline} via `PresenceController.SetPresence`.

## Admin (§13)

`GET /api/v1/admin/sessions` — cross-**organization** oversight via
`AdminService.ListAllSessions` (`SessionRepo.ListAll`, all orgs). Gated at the router
by `RequireSuperAdmin()` — only a platform `super_admin` (resolved from the JWT `role`
claim, §4.3) reaches it; org-scoped callers cannot. User / org / member management lives
entirely in the frontend's better-auth **admin** + **organization** plugins
(`/api/auth/admin/*`, §12) — the gateway no longer serves any `/auth/*` surface.

## Media (cross-cutting)

Media send types (image/video/audio/document/sticker) and image status return
`501 not_implemented` consistently. Domain helper: `domain.ErrNotImplemented`.

## Tests

- Handlers: `internal/http/handlers/resources_test.go` — httptest + fake services
  per group: happy path, validation propagation, auth (401) failures, and the 501
  media/not-implemented cases.
- Services: `internal/service/resources_test.go` — sqlmock-backed store + fake
  live ports: org-ownership rejection, validation, live delegation, and the
  nil-port / image-status `not_implemented` fallbacks.
- Store: `GroupRepo.ListBySession` covered in `internal/store/repos_more_test.go`.
