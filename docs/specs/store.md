# Store — MySQL app-data repositories (`internal/store`)

Status: implemented (R1).

The app-data persistence layer for the WA-domain plane. Plain `database/sql` (no ORM), one repo
type per aggregate, each method returning `internal/domain` types. The gateway is the **sole
writer** of these tables; the frontend reads them read-only via Drizzle ([`frontend.md`](frontend.md)
§ hybrid reads). Masterplan §6, §7.

## Ownership — `organization_id`, not `tenant_id`

Every owned resource is keyed by **`organization_id`** (a better-auth organization id), reached
through org membership — the v1 `tenant_id` / `tenants` mirror and the custom `api_keys` table are
**gone**. `created_by_user_id` is retained for audit. A personal org is auto-created per user on
signup, so solo use is a one-member org. The gateway authorizes from JWT claims /
api-key `reference_id`, never by joining `member` on the hot path ([`trust-model.md`](trust-model.md)).

## Tables & repos

| Table | Repo | Owning key |
|---|---|---|
| `gateways` | `GatewayRepo` | (registry; one self-row in v2) |
| `wa_sessions` | `SessionRepo` | `organization_id` (+ `gateway_id` pin) |
| `webhooks` | `WebhookRepo` | `organization_id` |
| `webhook_deliveries` | `WebhookDeliveryRepo` | via `webhook_id` |
| `whatsapp_identities` | `IdentityRepo` | global (central identity, canonical LID) |
| _(no contacts table)_ | `ContactRepo` | projection over identities + chats(DM) + members, scoped via `session_id` |
| `whatsapp_groups` | `GroupRepo` | global |
| `whatsapp_group_members` | `GroupMemberRepo` | identity↔group pivot (role + `tag`), via `session_id` |
| `chats` | `ChatRepo` | via `session_id` |
| `messages` | `MessageRepo` | via `session_id` |
| `poll_votes` | `PollVoteRepo` | via `session_id` |
| `outbox` | `OutboxRepo` | `organization_id` (idempotency) |
| `event_log` | `EventLogRepo` | `organization_id` |

`apikey` and the other better-auth tables are **not** in this repo set — they are frontend-owned
(Drizzle). `APIKeyRepo` here is **read-only** (`GetByHash`) and used solely by `internal/authz`
to verify keys ([`api-keys.md`](api-keys.md)); it is not a key-management repo.

`Store` (`store.go`) aggregates the repos; `New(db *sql.DB)` builds the set. Org-scoped lists are
`ListByOrg(ctx, organizationID)` (sessions, webhooks); session-scoped tables resolve their owning
org via `wa_sessions`.

## v2 DDL highlights (`migrations/0001_init.up.sql`)

The v1 `0001_init` + `0002_wmstore` migrations were **dropped** and replaced by a single fresh v2
`0001_init` (pre-release DB reset, no backfill). Conventions: `utf8mb4`/`utf8mb4_unicode_ci`,
epoch-ms `BIGINT` timestamps, `VARCHAR(64)` ULID PKs (or surrogate `BIGINT UNSIGNED AUTO_INCREMENT`).

```sql
CREATE TABLE gateways (            -- registry; one self-row in v2, rows added when sharding
  id VARCHAR(64) PRIMARY KEY,      -- = GATEWAY_ID
  label VARCHAR(255) NULL, base_url TEXT NULL, last_seen_at BIGINT NULL,
  created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
);

CREATE TABLE wa_sessions (
  id VARCHAR(64) PRIMARY KEY,
  organization_id    VARCHAR(64) NOT NULL,   -- better-auth org id (owner)
  created_by_user_id VARCHAR(64) NULL,       -- audit
  gateway_id         VARCHAR(64) NOT NULL,   -- which gateway holds this session's keystore
  ... status / wa_jid / is_admin_session / rate_per_min / ...
  KEY idx_sessions_org (organization_id),
  KEY idx_sessions_gateway (gateway_id),
  UNIQUE KEY uq_sessions_jid (wa_jid)
);
-- webhooks/webhook_deliveries/whatsapp_*/chats/messages/poll_votes/outbox/event_log follow §7
```

- **`organization_id`** replaces v1 `tenant_id` on every owned table; `webhooks`, `event_log`,
  `outbox` carry it directly.
- **`gateways` + `wa_sessions.gateway_id`** are new in v2 — the session-pinning seam
  ([`whatsmeow-store.md`](whatsmeow-store.md), masterplan §4.5).
- **No `wmstore_*` in MySQL.** The whatsmeow keystore is gateway-local SQLite, auto-migrated by
  `sqlstore` — it is no longer part of this migration set.
- better-auth's own tables (`user`/`session`/`apikey`/`organization`/`member`/…) are **not**
  defined here; they are frontend-owned (drizzle-kit). Match `organization_id`/`user_id` lengths
  to better-auth ids (`VARCHAR(64)`).

## Migrations tooling — the binary applies them

The gateway **binary** owns the WA-data plane migrations via **golang-migrate** embedded over
`migrations/` (`source/iofs`, `database/mysql`). There is **no standalone migrate CLI**: run
`server migrate up` / `server migrate down` (`cmd/server/main.go`). The Makefile `migrate` target
invokes the binary. The auth plane is migrated separately by drizzle-kit in the frontend
(masterplan §19 #5).

## Decisions (carried from v1, still apply)

- **Upserts** via `ON DUPLICATE KEY UPDATE` on the natural unique key; capture upserts use
  `COALESCE(VALUES(col), col)` so a sparse later sighting never wipes a known value (e.g. a
  resolved push name survives a later nameless sighting); `chats.last_message_at` only moves
  forward via `GREATEST`.
- **Field ownership / no clobber.** Content upserts omit fields with dedicated mutators
  (`messages.status/edited/deleted`, `chats` user flags), so a redelivered capture can't regress a
  receipt.
- **Timestamps** are caller-supplied epoch-ms `int64` (`domain.NowMs()`) — repos never call the
  clock, staying deterministic/testable.
- **NULL/JSON.** Nullable columns are `*T`; nullable JSON binds through `nullableJSON`; JSON reads
  as opaque `json.RawMessage` or typed structs (`permissions`, `retry_policy`, `media_meta`,
  `custom_headers`, `events`).
- **Message ids.** `messages.id` is a generated `msg_<ULID>` string, not an auto-incrementing
  integer. It stays lexicographically sortable for cursor pagination while avoiding a single
  hot monotonic database counter under high write throughput.
- **Cursor pagination** opaque over the sortable `id` (`Page[T]{Items, NextCursor}`); limits
  clamp to `[1,200]` (default 50); bad cursor → `validation_error`.
- **Error mapping.** `sql.ErrNoRows` and zero-rows-affected updates/deletes → `domain.ErrNotFound`;
  other DB errors wrapped with `%w` + a `store: <op>` prefix.
- **Concurrency.** Work-claim queries (`OutboxRepo.ClaimQueued`, `WebhookDeliveryRepo.ClaimDue`)
  are documented as the multi-instance upgrade point (`FOR UPDATE SKIP LOCKED`).

## How it's tested

`go-sqlmock` (regexp matcher) drives every repo — SQL construction, arg binding, row scanning into
`domain` (incl. `*T` nullables + typed JSON), cursor pagination, and `ErrNoRows`/zero-rows →
`not_found` mapping. `CGO_ENABLED=0 go test ./internal/store/...`.
