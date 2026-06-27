# Contacts

Status: implemented (Phase 3, stage "resource handlers").

Scope: the found-users feature: identity resolution, group membership with a
per-group member tag, where-found queries, plus the live on-WhatsApp / picture /
about / block sub-resources.

### Data model

A **single central identity table** (`whatsapp_identities`, keyed by the canonical
non-AD LID) is the source of truth for a person — name (push name), phone, business
name. There is **no per-session contacts table**. "Found users" is a projection:

- a person is **found in a DM** when the session has a `chats` row (`type='dm'`)
  whose peer JID is their LID or phone JID;
- a person is **found in a group** via the `whatsapp_group_members` PIVOT
  (identity ↔ group), which also carries their `role` and a per-group **`tag`**
  (the second per-group identity WhatsApp shows beside the push name — often an
  obfuscated phone for anonymous members).

A DM-only person is simply an identity with no pivot row. Ownership is by
**`organization_id`**: contacts/identities are reached through a session that
belongs to the caller's active org. The frontend renders the surface by reading
these WA tables **directly and read-only via Drizzle** (§6.2); all live
sub-resources (check / picture / about / block) go through the gateway API, the
single writer.

## Endpoints (§13)

| Method | Path | Backed by |
|---|---|---|
| GET | `/sessions/{session}/contacts` | store (`ContactRepo.List`) — projection over identities; filters `?source=dm\|group`, `?group={jid}`, `?q=` |
| GET | `/sessions/{session}/contacts/{lid}` | store — identity + DM (from chats) + `groups[]` (per-group `tag` from the membership pivot) |
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
{ "identity": { "lid": "...", "name": "push name", "phoneNumber": "62...", "source": "group" },
  "dm": true,
  "groups": [ { "jid": "123@g.us", "name": "Team", "tag": "Al", "role": "admin", "lastSeen": 1719 } ] }
```

- The identity is the central `whatsapp_identities` row (push name preferred); the
  list/detail `source` is derived — `dm` when a direct chat exists, else `group`.
- `dm` is derived from the `chats` table (a `type='dm'` row whose peer is the
  identity's LID or phone JID), not a stored flag.
- `tag` is per-group, from `whatsapp_group_members.tag`.
- Identities are keyed by the **canonical non-AD LID** (the `:device` suffix is
  stripped at capture/backfill); `phone_number` / `phone_jid` hold the resolved
  phone. The old per-session `whatsapp_contacts` table no longer exists.

## Live ops boundary

The live calls (`check`, `picture`, `about`, `block`/`unblock`) are delegated to
the `service.ContactDirectory` port, satisfied in production by `wa.LiveOps` (a
manager-backed adapter resolving the per-session `*whatsmeow.Client`). When the
session has no connected client the adapter returns `not_implemented` (the same
behavior as the outbound send path's session-routing client for a session without
a live connection — see outbound-pipeline.md "Session routing").
