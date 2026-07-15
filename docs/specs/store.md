# Store — MySQL app-data repositories (`internal/store`)

Status: implemented (R1).

The app-data persistence layer for the WA-domain plane. Repositories expose `internal/domain`
types and mostly use generated `sqlc` query bindings over `database/sql` internally; OAuth/OIDC
repos use the same plain `database/sql` repo boundary directly because their migration was added
after the sqlc baseline. There is still no ORM and no ORM-owned migrations. The gateway is the
**sole writer** of these tables; the frontend reads
them read-only via Drizzle ([`frontend.md`](frontend.md) § hybrid reads). Masterplan §6, §7.

## Ownership — `organization_id`, not `tenant_id`

Every owned resource is keyed by **`organization_id`** (a better-auth organization id), reached
through org membership — the v1 `tenant_id` / `tenants` mirror and the custom `api_keys` table are
**gone**. `created_by_user_id` is retained for audit. A personal org is auto-created per user on
signup, so solo use is a one-member org. The gateway authorizes from JWT claims /
api-key `reference_id`, never by joining `member` on the hot path ([`trust-model.md`](trust-model.md)).

## Tables & repos

| Table | Repo | Owning key |
|---|---|---|
| `gateways` | `GatewayRepo` | (registry; lifecycle + routing table — the router reads it) |
| `wa_sessions` | `SessionRepo` | `organization_id` (+ `gateway_id` pin, now authoritative for routing) |
| `webhooks` | `WebhookRepo` | `organization_id` |
| `webhook_deliveries` | `WebhookDeliveryRepo` | via `webhook_id` |
| `whatsapp_identities` | `IdentityRepo` | global (central identity, canonical LID) |
| _(no contacts table)_ | `ContactRepo` | projection over identities + chats(DM) + members, scoped via `session_id` |
| `whatsapp_groups` | `GroupRepo` | global |
| `whatsapp_group_members` | `GroupMemberRepo` | identity↔group pivot (role + `tag`), via `session_id` |
| `chats` | `ChatRepo` | via `session_id` |
| `messages` | `MessageRepo` | via `session_id` (inbound captures **and** the gateway's own sends — see below) |
| `polls` | `PollRepo` | via `session_id` (poll-creation options, so votes resolve to text) |
| `poll_votes` | `PollVoteRepo` | via `session_id` |
| `outbox` | `OutboxRepo` | `organization_id` (idempotency) |
| `event_log` | `EventLogRepo` | `organization_id` |
| `messages` + `event_log` + `webhook_deliveries` | `RetentionRepo` | global maintenance (bounded retention batches) |
| `backfill_imports` | `BackfillImportRepo` | via `session_id` (import job status + once/24h quota) |
| `oauth_clients` | `OAuthClientRepo` | `organization_id` |
| `oauth_grants` | `OAuthGrantRepo` | `organization_id` |
| `oauth_refresh_tokens` | `OAuthRefreshTokenRepo` | `organization_id` |
| `oauth_signing_keys` | `OAuthSigningKeyRepo` | global OIDC keyset |

`apikey` and the other better-auth tables are **not** in this repo set — they are frontend-owned
(Drizzle). `APIKeyRepo` here is **read-only** (`GetByHash`) and used solely by `internal/authz`
to verify keys ([`api-keys.md`](api-keys.md)); it is not a key-management repo.

`Store` (`store.go`) aggregates the repos; `New(db *sql.DB)` builds the set. The generated sqlc
package lives under `internal/store/storedb` and is kept behind the repo boundary; callers should
not import generated DB-shaped rows directly. Org-scoped lists are
`ListByOrg(ctx, organizationID)` (sessions, webhooks); session-scoped tables resolve their owning
org via `wa_sessions`.

OAuth/OIDC provider state is added by `migrations/0007_oidc_provider`: org-owned clients, durable
grants, rotating refresh-token rows, and the shared OIDC signing keyset. The public key material is
listed from `oauth_signing_keys`; private JWKs are AES-GCM encrypted by `internal/oidp` before the
repo persists them.

## v2 DDL highlights (`migrations/0001_init.up.sql`)

The v1 `0001_init` + `0002_wmstore` migrations were **dropped** and replaced by a single fresh v2
`0001_init` (pre-release DB reset, no backfill). Conventions: `utf8mb4`/`utf8mb4_unicode_ci`,
epoch-ms `BIGINT` timestamps, `VARCHAR(64)` ULID PKs (or surrogate `BIGINT UNSIGNED AUTO_INCREMENT`).

```sql
CREATE TABLE gateways (            -- registry + lifecycle; the router reads it to route
  id VARCHAR(64) PRIMARY KEY,      -- = GATEWAY_ID
  label VARCHAR(255) NULL, base_url TEXT NULL, last_seen_at BIGINT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'active',   -- joining|active|draining|drained|unreachable (0004)
  session_count INT UNSIGNED NOT NULL DEFAULT 0,  -- live session count, written by heartbeat (0004)
  capacity INT UNSIGNED NULL,                      -- soft cap for placement; NULL = unbounded (0004)
  created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
  KEY idx_gateways_status_seen (status, last_seen_at)  -- (0004) active-gateway / stale-heartbeat scans
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
-- webhooks/webhook_deliveries/whatsapp_*/chats/messages/polls/poll_votes/outbox/event_log follow §7
-- polls (0005): UNIQUE (session_id, poll_message_id); options JSON; selectable_count.
-- poll recap metadata (0006): optional end_time, hide_votes, and recap_emitted_at.
--   It is the canonical source of a poll's option list so incoming votes (option
--   hashes) resolve to text, and the durable guard for one poll.recap event after
--   a timed poll closes.
```

- **`organization_id`** replaces v1 `tenant_id` on every owned table; `webhooks`, `event_log`,
  `outbox` carry it directly.
- **`gateways` + `wa_sessions.gateway_id`** are the session-pinning seam
  ([`whatsmeow-store.md`](whatsmeow-store.md), masterplan §4.5); with the central router (Increment A)
  `gateways` is now the live **routing table** the router reads, and `wa_sessions.gateway_id` (already
  `NOT NULL`) is **authoritative for routing** ([`router.md`](router.md), [`session-manager.md`](session-manager.md)).
- **No `wmstore_*` in MySQL.** The whatsmeow keystore is gateway-local SQLite, auto-migrated by
  `sqlstore` — it is no longer part of this migration set.
- better-auth's own tables (`user`/`session`/`apikey`/`organization`/`member`/…) are **not**
  defined here; they are frontend-owned (drizzle-kit). Match `organization_id`/`user_id` lengths
  to better-auth ids (`VARCHAR(64)`).

## Gateways registry lifecycle (`migration 0004_gateways_lifecycle`)

Migration **`0004_gateways_lifecycle.{up,down}.sql`** adds lifecycle/accounting to the existing
`gateways` table (Layer 1 of the central-router work — [`router.md`](router.md),
[`session-manager.md`](session-manager.md)):

- **`status`** `VARCHAR(16) NOT NULL DEFAULT 'active'` — `joining | active | draining | drained |
  unreachable`.
- **`session_count`** `INT UNSIGNED NOT NULL DEFAULT 0` — live sessions on the gateway, refreshed by
  its heartbeat.
- **`capacity`** `INT UNSIGNED NULL` — soft placement cap; `NULL` = unbounded.
- **`INDEX idx_gateways_status_seen (status, last_seen_at)`** — backs active-gateway selection,
  placement, and stale-heartbeat (`unreachable`) detection.

`GatewayRepo` gains the lifecycle methods:

- **`Heartbeat`** — touch `last_seen_at` + `session_count` (the 30s gateway loop).
- **`SetStatus`** — move through the lifecycle (`joining → active`, `draining → drained` on SIGTERM).
- **`ListActive`** — the `active` gateways (gateway-agnostic routing target).
- **`PickForPlacement`** — choose the least-loaded `active` gateway for a new session
  (`POST /sessions` placement).

`SessionRepo` gains **`CountByGateway`** (feeds `session_count` in the heartbeat).
`wa_sessions.gateway_id` is unchanged (already `NOT NULL`) and is now **authoritative for routing**.

## Retention indexes (`migration 0009_retention_indexes`)

The retention worker is a global gateway-maintenance writer, not an
organization-scoped request path. Migration **`0009_retention_indexes`** adds
the index support for its bounded deletes:

- `webhook_deliveries(status, created_at, id)` for terminal delivery history;
- `webhook_deliveries(event_id, status)` to protect events still needed by an
  active webhook retry;
- `messages(timestamp, id)` and `event_log(created_at, id)` for ordered
  delete batches.

No table ownership changes: `RetentionRepo` removes at most a bounded batch per
statement and repeats until the pass is caught up. Its deletion order is
terminal webhook-delivery rows, messages, then event-log rows that have no
`pending` or retryable `failed` webhook delivery. This keeps webhook retries
able to reload their event body while still bounding completed history.

A session pinned to a gateway that is missing / not `active` / has a stale heartbeat is a *stranded*
session: the router returns the new **`gateway_unavailable` (HTTP 503)** domain error rather than
hanging. After running the migration, `cd web && pnpm db:introspect` refreshes the read-only WA
Drizzle models.

## Migrations tooling — the binary applies them

The gateway **binary** owns the WA-data plane migrations via **golang-migrate** embedded over
`migrations/` (`source/iofs`, `database/mysql`). There is **no standalone migrate CLI**: run
`server migrate up` / `server migrate down` (`cmd/server/main.go`). The Makefile `migrate` target
invokes the binary. The auth plane is migrated separately by drizzle-kit in the frontend
(masterplan §19 #5).

`sqlc` consumes the same migration SQL as schema input plus named queries in
`internal/store/queries/`; it generates typed query methods in `internal/store/storedb/`.
Regenerate with `make sqlc` after changing store query files or WA migrations. The generated types
are DB-shaped by design; repo methods map nullable values, JSON blobs, generated enums, and
`RowsAffected` / `LastInsertId` results back to the stable `domain` API.

The gateway also has read-only hot-path checks against frontend-owned Better Auth tables
(`apikey`, `organization`). Those tables are still migrated only by the frontend Drizzle
toolchain; `internal/store/sqlc_schema/auth.sql` is a sqlc-only schema stub so the gateway's typed
read queries can compile without making the gateway a writer or migration owner for auth tables.

## Decisions (carried from v1, still apply)

- **Upserts** via `ON DUPLICATE KEY UPDATE` on the natural unique key; capture upserts use
  `COALESCE(VALUES(col), col)` so a sparse later sighting never wipes a known value (e.g. a
  resolved push name survives a later nameless sighting); `chats.last_message_at` only moves
  forward via `GREATEST`.
- **Read-time identity resolution.** Message reads enrich rows from
  `whatsapp_identities` rather than storing display data on the message: a left
  join resolves the sender's `sender_name` (by `sender_lid`), and the service
  layer resolves the body's `@`-mentions to `mentionNames` (one
  `IdentityRepo.NamesForMentions` batch per page, keyed by the mention's user-part
  so it lines up with the `@<number>` token in the body). Both are read-only
  projections — never stored columns — so a later name change is reflected without
  rewriting messages.
- **Chat-list projection.** `ChatRepo.ListBySession` is an inbox view, not an
  address book: it omits chat rows with no `last_message_at`, orders by
  `last_message_at DESC, id DESC`, and resolves display names from
  `whatsapp_groups` for groups or `whatsapp_identities` for DMs before falling
  back to `chats.name`. Contacts that were only found in groups stay in the
  contacts/new-chat flow until a message exists. DM chat reads also expose
  `aliases` from the matched identity (`lid` + linked `phone_jid`) so clients
  can merge rows observed through both WhatsApp address forms.
- **DM alias resolution.** Messages may be captured under either the contact's
  LID or phone JID depending on the event/import source. `MessageRepo.ListByChat`
  expands a DM chat id through `whatsapp_identities` and returns messages stored
  under either alias, so opening either address shows one logical timeline.
  Write paths also canonicalize phone-JID DM chats to the mapped LID when exactly
  one `whatsapp_identities.phone_jid` match exists; identity upserts merge any
  existing phone-JID chat/message/poll rows into the LID row once that mapping is
  discovered. Ambiguous or unknown phone JIDs are left unchanged.
- **Field ownership / no clobber.** Content upserts omit fields with dedicated mutators
  (`messages.status/edited/deleted`, `chats` user flags), so a redelivered capture can't regress a
  receipt. The receipt-specific message mutator is monotonic (`pending` → `sent` →
  `delivered` → `read` → `played`), preserves terminal `failed`, and only increases
  a supplied `ack_level`. Duplicate/stale receipts and unknown message IDs are
  successful no-ops; the latter are expected for pre-capture history and traffic
  from other linked devices.
- **Retention is a gateway-owned maintenance write.** When `RETENTION_DAYS > 0`,
  the gateway schedules one shared-Redis-database, deduplicated daily `retention:prune`
  task. The worker uses the cutoff stored in that task and deletes in bounded
  batches, so it neither locks a large historical range nor competes indefinitely
  with foreground writes. `RETENTION_DAYS=0` disables scheduling (keep forever);
  negative values are invalid configuration. The prune targets are old
  `messages`, `event_log`, and terminal (`delivered` / `dead`) rows in
  `webhook_deliveries`. It intentionally preserves `pending` and retryable
  `failed` delivery rows regardless of age because their payload, attempts, and
  retry time remain operational state, and retains their referenced event-log
  rows until those deliveries are terminal. Event-log retention otherwise bounds
  stream replay: a `since` cursor that is older than the retained history
  replays from the oldest remaining event.
- **Timestamps** are caller-supplied epoch-ms `int64` (`domain.NowMs()`) — repos never call the
  clock, staying deterministic/testable.
- **NULL/JSON.** Nullable columns are `*T`; nullable JSON binds through `nullableJSON`; JSON reads
  as opaque `json.RawMessage` or typed structs (`permissions`, `retry_policy`, `media_meta`,
  `custom_headers`, `events`).
- **`messages` has two writers.** The inbound pipeline writes received messages
  (`direction='in'`, plus `from_me`/`out` rows for sends echoed from the account's
  *other* devices); the outbound pipeline writes the gateway's own sends
  (`from_me=true`, `direction='out'`, `status='sent'`) via
  `MessageRecorderAdapter` on each successful dispatch — see
  [`outbound-pipeline.md`](outbound-pipeline.md). Both go through
  `MessageRepo.Upsert` keyed by `(session_id, wa_message_id)`, so the two paths
  reconcile onto one row rather than duplicating (a self-send and any later echo
  of it collapse to the same message). A **third writer** is the crypt15 backup
  import (`BackupImportService`), which upserts historical messages/chats through
  the same repos — also idempotent by `(session_id, wa_message_id)`, so an import
  merges with live capture (see [`backfill-import.md`](backfill-import.md)).
  Backup imports never key identities or group members by phone JID; unresolved
  phone-only people are skipped until a canonical LID is known.
- **Poll vote idempotency.** `poll_votes` keeps vote history, but WhatsApp event
  replay should not append the same vote twice. The table has a replay key on
  `(session_id, poll_message_id, voter_lid, timestamp)`, and
  `PollVoteRepo.Insert` uses `INSERT IGNORE` so duplicate delivery of the same
  poll-update event is a no-op while later re-votes with a new timestamp remain
  separate history rows. `voter_lid` is the normalized per-voter key supplied by
  the inbound adapter: canonical LID when present, otherwise the sender phone JID.
  It must not be empty for real votes, otherwise same-timestamp votes from
  different voters would share the replay key and recaps could not separate voters.
- **Poll close recaps.** `polls.end_time` stores WhatsApp's poll close time in
  epoch-ms, `hide_votes` mirrors the poll privacy flag, and
  `recap_emitted_at` is the durable exactly-once claim for the synthetic
  `poll.recap` event. Redis holds a best-effort sorted-set timer for low-latency
  wakeups, but MySQL remains authoritative; a periodic sweep over
  `idx_poll_recap_due (end_time, recap_emitted_at)` catches missed Redis entries
  after restarts.
- **`backfill_imports` is the import quota's source of truth.** A user backup
  import is durably tracked here (status + counts + schema fingerprint); the
  once/24h-per-session limit is enforced by `LastSuccessAt` and the concurrency
  guard by `HasRunningSince`, so the quota survives restarts. Owned via
  `session_id`; `super_admin` bypasses the quota.
- **Message ids.** `messages.id` is a generated `msg_<ULID>` string, not an auto-incrementing
  integer. It stays lexicographically sortable for cursor pagination while avoiding a single
  hot monotonic database counter under high write throughput. Message timeline
  pages are returned newest-first; the next cursor is the last row in the page
  and loads older rows with `id < cursor`.
- **Cursor pagination** uses opaque resource-specific cursors
  (`lastMessageAt:id` for the chat inbox, sortable message ids for messages,
  numeric ids elsewhere); limits clamp to `[1,200]` (default 50); bad cursor →
  `validation_error`.
- **Error mapping.** `sql.ErrNoRows` and zero-rows-affected updates/deletes → `domain.ErrNotFound`;
  other DB errors wrapped with `%w` + a `store: <op>` prefix.
- **Concurrency.** `WebhookDeliveryRepo.ClaimDue` selects due rows with `FOR UPDATE SKIP LOCKED`
  and advances `next_retry_at` to a claim lease in the same transaction. Leases are staggered
  by each row's position in the sequential batch, using twice the production HTTP timeout per
  item. Concurrent
  dispatchers therefore receive disjoint batches, while a crashed worker's rows automatically
  become eligible again after the lease. `webhook_deliveries` also has a unique
  `(webhook_id,event_id)` key, making fan-out enqueue idempotent at the database boundary.
  Outbox ownership uses the same database-first rule: `ClaimByID` is a single
  compare-and-set from `queued` (initial attempt), `failed` (retry), or a `sending` row older
  than a caller-supplied lease cutoff to `sending`, incrementing attempts and returning whether
  this process won. Fresh `sending` rows cannot be stolen; stale rows are reclaimable after a
  worker crash instead of remaining stranded forever. `ClaimQueued` locks an ordered page with
  `FOR UPDATE SKIP LOCKED`, applies that CAS to every row, and commits only the complete batch.
  Session-specific batch claims apply their session predicate inside that locking query; they
  never claim another session's row and discard it after the transition.
  Duplicate queue tasks and worker replicas therefore cannot simultaneously own one outbox row;
  a sent/fresh-sending/missing row is a normal non-claim rather than an error.
- **No retained media bytes.** An outbound media send carries either inline base64 in
  `outbox.payload.media.data` or a URL in `outbox.payload.media.url`. `OutboxRepo.UpdateStatus`
  strips `$.media.data` (via `JSON_REMOVE`) when the row is marked `sent`, so inline bytes live
  only until the send is dispatched. URL sends retain only the URL for retry. A `failed` row keeps
  the payload so the async worker can retry.

## How it's tested

`go-sqlmock` (regexp matcher) drives every repo — generated SQL execution, arg binding, row mapping
into `domain` (incl. `*T` nullables + typed JSON), cursor pagination, and `ErrNoRows`/zero-rows →
`not_found` mapping. `CGO_ENABLED=0 go test ./internal/store/...`.
