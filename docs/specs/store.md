# Store — MySQL app-data repositories

Package: `internal/store` · module `github.com/ramaadi/quick-whatsapp-gateway/internal/store`

## Scope

The app-data persistence layer for every table in masterplan §5. Plain
`database/sql` (no ORM), one repo type per aggregate, each method returning
`internal/domain` types. This package **owns** the concrete MySQL repo
implementations; per the build's import rules it is imported by no one during the
parallel phase and is wired in by Phase 3.

Tables covered (one repo each unless noted):

| Table | Repo |
|---|---|
| `tenants` | `TenantRepo` |
| `wa_sessions` | `SessionRepo` |
| `api_keys` | `APIKeyRepo` |
| `webhooks` | `WebhookRepo` |
| `webhook_deliveries` | `WebhookDeliveryRepo` |
| `whatsapp_identities` | `IdentityRepo` |
| `whatsapp_contacts` | `ContactRepo` |
| `whatsapp_groups` | `GroupRepo` |
| `whatsapp_group_members` | `GroupMemberRepo` |
| `chats` | `ChatRepo` |
| `messages` | `MessageRepo` |
| `poll_votes` | `PollVoteRepo` |
| `outbox` | `OutboxRepo` |
| `event_log` | `EventLogRepo` |

`Store` (in `store.go`) aggregates all repos for convenient wiring; `New(db
*sql.DB)` builds the full set. Each repo is also independently constructable via
its `New<Repo>(db)`.

## Key types & interfaces

- **`dbExecQuerier`** (consumer interface) — the `ExecContext` /
  `QueryContext` / `QueryRowContext` subset of `*sql.DB` and `*sql.Tx`. Every
  `New<Repo>` takes this, so a repo runs against a raw DB or inside a
  transaction, and is trivially satisfied by `go-sqlmock`'s `*sql.DB`.
- **`Page[T]`** — `{ Items []T; NextCursor string }`, the result of every
  cursor-paginated list (`ListByChat`, `ChatRepo.ListBySession`,
  `ContactRepo.List`).
- **Method shapes** (representative):
  - `SessionRepo`: `Create, Get, GetByJID, ListByTenant, ListAll, Update, UpdateStatus, Delete`
  - `MessageRepo`: `Upsert` (by `session_id+wa_message_id`), `GetByWAID, UpdateStatus, MarkEdited, MarkDeleted, ListByChat`
  - `ChatRepo`: `Upsert, Get, ListBySession, UpdateFlags, Delete`
  - `IdentityRepo`: `Upsert` (by `lid`), `GetByLID`
  - `ContactRepo`: `Upsert` (by `session_id+lid`), `BumpSeen, Get, List(ContactFilter, cursor, limit)`
  - `GroupRepo`: `Upsert, GetByJID`; `GroupMemberRepo`: `Upsert, ListByGroup, ListByContact, Remove`
  - `APIKeyRepo`: `Create, Get, GetByPrefix, ListByTenant, UpdateLastUsed, Revoke, Rotate`
  - `WebhookRepo`: `Create, Get, ListByTenant, ListActiveForEvent, Update, Delete`
  - `WebhookDeliveryRepo`: `Enqueue, ClaimDue, MarkDelivered, MarkFailed, MarkDead`
  - `OutboxRepo`: `Insert, Get, GetByIdempotency, UpdateStatus, ClaimQueued`
  - `EventLogRepo`: `Append, ListSince(tenant, session, afterID, limit), GetByEventID`
  - `TenantRepo`: `Upsert, GetByID, GetByEmail`
  - `PollVoteRepo`: `Insert, ListByPoll`

## Decisions

- **Upserts** use MySQL `ON DUPLICATE KEY UPDATE` keyed on the table's natural
  unique key (e.g. `messages` on `(session_id, wa_message_id)`, `whatsapp_contacts`
  on `(session_id, lid)`). Capture upserts use `COALESCE(VALUES(col), col)` so a
  sparse later sighting never wipes a previously-known value (push name, phone,
  group subject). `whatsapp_contacts.seen_in_dm` is OR-ed in; `chats.last_message_at`
  only moves forward via `GREATEST`.
- **Field ownership / no clobber.** Content upserts deliberately omit fields that
  have dedicated mutators: `MessageRepo.Upsert` does not touch
  `status/ack_level/edited/deleted` (owned by `UpdateStatus/MarkEdited/MarkDeleted`),
  so a redelivered capture event can't regress a delivery receipt. `ChatRepo.Upsert`
  leaves the user flags (`archived/pinned/muted_until`) to `UpdateFlags`.
- **Timestamps** are epoch-ms `int64` supplied by the caller
  (`domain.NowMs()`); the repos never call the clock, keeping them deterministic
  and testable.
- **NULL handling.** Nullable columns are `*T` in `domain`; nullable JSON columns
  bind through `nullableJSON` (`nil`/empty → SQL `NULL`). JSON columns are read as
  `[]byte` and unmarshalled into `json.RawMessage` (opaque: `mentions`, `payload`,
  `selected_options`, `raw_json`) or typed structs (`permissions`, `retry_policy`,
  `media_meta`, `custom_headers`, `events`).
- **Cursor pagination** is opaque over the surrogate `id` column: the cursor is
  the decimal id (callers must not parse it). `parseCursor`/`encodeCursor` are the
  only places that know the encoding; `pageFrom` sets `NextCursor` to the last
  id only when the page filled to `limit`. Limits clamp to `[1, 200]` (default 50).
  A malformed cursor is a `validation_error` `domain.APIError`.
- **Error mapping.** `sql.ErrNoRows` from a single-row lookup maps to
  `domain.ErrNotFound` (`not_found` APIError). An `UPDATE`/`DELETE` that matches
  zero rows likewise maps to `not_found` (`affectedOrNotFound`); when the driver
  doesn't report affected rows the exec is treated as succeeded. All other DB
  errors are wrapped with `%w` and a `store: <op>` prefix.
- **Concurrency.** v1 is single-instance (§3), so the work-claim queries
  (`OutboxRepo.ClaimQueued`, `WebhookDeliveryRepo.ClaimDue`) use plain
  UPDATE-then-SELECT / SELECT rather than `FOR UPDATE SKIP LOCKED`. `ClaimQueued`
  tags the batch via the claim `updated_at` to read back exactly the rows it just
  transitioned. This is documented in the code as the v2 multi-instance upgrade
  point.

## Consumer interface for wiring (Phase 3)

Each `New<Repo>` accepts the local `dbExecQuerier`. Phase 3 passes the concrete
`*sql.DB` (or a `*sql.Tx`); both satisfy it. The convenience entrypoint is
`store.New(db *sql.DB) *Store`. Service-layer packages should depend on their own
narrow interfaces (Go consumer-interface convention) and accept the concrete
repos as implementations.

## How it's tested

`go-sqlmock` with the regexp query matcher drives every repo test (no live DB).
Tests assert on: SQL construction (the meaningful fragments — `ON DUPLICATE KEY
UPDATE`, `COALESCE`, `GREATEST`, join shapes, `ORDER BY id ASC LIMIT`), argument
binding order, row scanning into `domain` structs (including `*T` nullables, JSON
columns, and typed JSON like `permissions`/`media_meta`/`retry_policy`), cursor
pagination (full page → next cursor = last id; partial page → empty; bad cursor →
`validation_error`), and `ErrNoRows`/zero-rows-affected → `not_found` mapping.
Pure helpers (`normLimit`, `parseCursor`/`encodeCursor`, `pageFrom`,
`nullableJSON`, `prefixCols`) have direct table-driven tests. Representative repos
required by the task — sessions, messages, contacts, api_keys, event_log, outbox —
are covered in depth; the rest (tenant, webhook, webhook_delivery, identity,
group, group_member, chat, poll_vote) have create/upsert + scan + not-found
coverage. `CGO_ENABLED=0 go test ./internal/store/...` passes; ~64% statement
coverage.
