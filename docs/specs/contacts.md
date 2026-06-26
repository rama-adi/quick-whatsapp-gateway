# Contacts

Status: implemented (Phase 3, stage "resource handlers").

Scope: the found-users feature: identity resolution, per-account contacts, group membership with per-group nicknames, where-found queries, plus the live on-WhatsApp / picture / about / block sub-resources.

The capture/resolution logic is **stable** (unchanged from v1); only ownership moved
from `tenant_id` to **`organization_id`**. Contacts/identities are reached through a
session that belongs to the caller's active org. The frontend renders the contacts
surface by reading these WA tables **directly and read-only via Drizzle** (§6.2) for
fast listing; all live sub-resources (check / picture / about / block) still go through
the gateway API, which is the single writer.

## Endpoints (§13)

| Method | Path | Backed by |
|---|---|---|
| GET | `/sessions/{session}/contacts` | store (`ContactRepo.List`) — filters `?source=dm\|group`, `?group={jid}`, `?q=` |
| GET | `/sessions/{session}/contacts/{lid}` | store — identity + DM + `groups[]` (push name preferred, per-group nickname from the membership pivot) |
| GET | `/sessions/{session}/contacts/check?phone=` | live `ContactDirectory.IsOnWhatsApp` |
| GET | `/sessions/{session}/contacts/{jid}/picture` | live `ContactDirectory.ProfilePicture` |
| GET | `/sessions/{session}/contacts/{jid}/about` | live `ContactDirectory.About` (returns `{about}`) |
| POST | `/sessions/{session}/contacts/{jid}/block` · `/unblock` | live `ContactDirectory.SetBlocked` |

## Service shape

`service.ContactService` over `*store.Store` + a `ContactDirectory` live port (nil
=> live sub-resources return `not_implemented`). Every method first verifies the
session exists and belongs to the caller's organization (foreign org => `not_found`).

`GET /contacts/{lid}` returns `service.ContactDetail`:

```json
{ "identity": { "lid": "...", "name": "push name" },
  "contact": { "lid": "...", "seenInDm": true, "messageCount": 12 },
  "dm": true,
  "groups": [ { "jid": "123@g.us", "name": "Team", "nickname": "Al", "role": "admin", "lastSeen": 1719 } ] }
```

- Push name comes from the global `whatsapp_identities` row (best-effort; a
  contact may exist before its identity).
- `nickname` is per-group, from `whatsapp_group_members.group_nickname`.

## Live ops boundary

The live calls (`check`, `picture`, `about`, `block`/`unblock`) are delegated to
the `service.ContactDirectory` port, satisfied in production by `wa.LiveOps` (a
manager-backed adapter resolving the per-session `*whatsmeow.Client`). When the
session has no connected client the adapter returns `not_implemented` (the same
behavior as the outbound send path's session-routing client for a session without
a live connection — see outbound-pipeline.md "Session routing").
