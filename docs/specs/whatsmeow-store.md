# whatsmeow Store

> ⚠️ **SUPERSEDED (v1).** This describes the custom **MySQL** whatsmeow store. v2 uses
> **SQLite** via whatsmeow's `sqlstore` (`modernc.org/sqlite`, gateway-local persistent
> volume) — the custom MySQL adapter is dropped. See
> [`../../masterplan-mvp.md`](../../masterplan-mvp.md) §6.1 and [`_V2-STATUS.md`](_V2-STATUS.md).
> To be rewritten/retired in **R2**.

Status: implemented.

The whatsmeow device keystore with two interchangeable backends behind one
selectable entrypoint (masterplan §4, recon `_recon-whatsmeow.md` §4).

## Packages

| Path | Package | Role |
|------|---------|------|
| `internal/wa/store` | `wastore` | Backend-agnostic selector: `Keystore` interface + `Open(ctx, driver, db, dsn, log)`. |
| `internal/wa/store/mysql` | `mysqlstore` | Hand-written MySQL implementation of every whatsmeow store interface. |
| `internal/wa/store/sqlite` | `sqlitestore` | Thin wrapper over `sqlstore` + modernc SQLite (zero-config fallback). |
| `migrations/0002_wmstore.{up,down}.sql` | — | `wmstore_*` table DDL for the MySQL backend. |

## Why a hand-rolled MySQL backend

`go.mau.fi/util/dbutil` only knows the Postgres and SQLite dialects, and
`go.mau.fi/whatsmeow/store/sqlstore` branches exclusively on those two. There is
no MySQL path through sqlstore (`dbutil.ParseDialect("mysql")` errors). To keep
whatsmeow device state in the gateway's primary MySQL database, `mysqlstore`
re-implements all the interfaces directly on `database/sql`:

- `?` positional placeholders (vs sqlstore's `$N` / `?N`).
- `INSERT ... ON DUPLICATE KEY UPDATE col=VALUES(col)` for upserts.
- `VARBINARY(n)` for fixed-size keys, `BLOB` for variable blobs, `TINYINT(1)`
  for booleans, `CHAR(36)` for the facebook UUID (stored as text).
- Conditional upserts (`app_state_sync_keys`, `privacy_tokens`) use
  `col=IF(VALUES(timestamp) > timestamp, VALUES(col), col)` to replicate the
  Postgres `... DO UPDATE ... WHERE excluded.ts > existing.ts` guard, which MySQL
  cannot express in the ON DUPLICATE clause directly.
- The PN↔LID resolving subqueries in `GetMessageSecret` / `GetPrivacyToken` are
  translated from `||`/`replace()` to `CONCAT()`/`REPLACE()`. Because MySQL has
  no positional `$N` reuse, the same JID string is passed multiple times (the Go
  caller expands the arg list).

## Key types / interfaces

### Consumer interface (`wastore.Keystore`)
The session manager (Phase 3) depends only on this:

```go
type Keystore interface {
    GetFirstDevice(ctx) (*store.Device, error)
    GetAllDevices(ctx) ([]*store.Device, error)
    GetDevice(ctx, jid) (*store.Device, error)
    NewDevice() *store.Device
    PutDevice(ctx, *store.Device) error
    DeleteDevice(ctx, *store.Device) error
}
```

It is a superset of `store.DeviceContainer` (which only declares
`PutDevice`/`DeleteDevice`) plus the loaders/factory the manager needs. Both
`*mysqlstore.Container` and `*sqlstore.Container` satisfy it.

### `wastore.Open`
```go
func Open(ctx, driver Driver, db *sql.DB, dsn string, log waLog.Logger) (Keystore, error)
```
- `DriverMySQL`: wraps the **caller-provided** `*sql.DB` (the app's MySQL pool)
  via `mysqlstore.NewContainer`. `dsn` ignored. Migrations must be applied first.
  Passing the pool in (rather than opening one here) keeps this package free of
  any import on `internal/store`.
- `DriverSQLite` (also accepts `"sqlite3"`): opens+upgrades the SQLite file at
  `dsn` via `sqlitestore.Open`. `db` ignored.

### `mysqlstore.Container` (DeviceContainer)
`GetFirstDevice` / `GetAllDevices` / `GetDevice` / `NewDevice` / `PutDevice` /
`DeleteDevice`, mirroring `sqlstore.Container`. On load/save it calls
`initializeDevice`, which wires the per-device stores via
`device.SetAllStores(inner)`, sets `device.LIDs = container.lid` (global LID
store), `device.Container`, and `device.Initialized`.

### `mysqlstore.mysqlStore` (per-device)
One instance per device JID. Implements all 12 per-device interfaces; verified by
compile-time assertions (`var _ store.IdentityStore = (*mysqlStore)(nil)`, … and
`var _ store.AllSessionSpecificStores = (*mysqlStore)(nil)`). Keeps the same
contact-info cache + prekey-generation locking strategy as sqlstore.

### `mysqlstore.lidMap` (global LIDStore)
Process-wide phone↔LID map with an in-memory cache, mirroring
`sqlstore.CachedLIDMap`. Rows store bare User parts; the server suffix
(`s.whatsapp.net` / `lid`) is reattached on read. `var _ store.LIDStore`.

### `sqlitestore.Open`
```go
func Open(ctx, dsn string, log) (*sqlstore.Container, error)
```
modernc registers its driver as `"sqlite"`; `dbutil.ParseDialect` accepts any
`sqlite*` prefix, so we `sql.Open("sqlite", dsn)` + `sqlstore.NewWithDB(db,
"sqlite", log)` + `Upgrade` — no driver alias needed. `Upgrade` requires foreign
keys, so the dsn must include `?_pragma=foreign_keys(1)`.

## Schema notes (`wmstore_*`)
Mechanically translated from `sqlstore/upgrades/00-latest-schema.sql` (v14):
all tables InnoDB/utf8mb4 with `ON DELETE/UPDATE CASCADE` FKs back to
`wmstore_device(jid)`. `wmstore_app_state_mutation_macs` FKs to the composite
`(jid, name)` of `wmstore_app_state_version`. `wmstore_lid_map` and
`wmstore_privacy_tokens` have no device FK (they outlive / are independent of a
device row), matching the upstream schema. Indexed string columns are
`VARCHAR(128)` (utf8mb4 bounded-length requirement) rather than unbounded TEXT;
128 was chosen over 255 so the four-column PK of `wmstore_message_secrets`
(`4*128*4 = 2048` bytes) stays under InnoDB's 3072-byte index-prefix limit —
`VARCHAR(255)` overflowed it (`4*255*4 = 4080`). Non-indexed free-text columns
(`wmstore_device.platform/business_name/push_name`, `wmstore_retry_buffer.format`)
remain `VARCHAR(255)`.

## Decisions / deviations
- `DoDecryptionTxn` runs `fn` inside a `database/sql` transaction but does **not**
  propagate that `*sql.Tx` through `ctx` (whatsmeow's interface gives us only a
  `func(ctx)`), so nested store calls inside `fn` use the pooled connection. This
  is best-effort atomicity at the store level; acceptable because the buffer
  tables are advisory de-dupe state, not crypto material.
- `MigratePNToLID` does the migrate-then-delete in one transaction; the
  per-process "already migrated" cache from sqlstore is omitted (idempotent SQL
  makes it unnecessary for correctness).

## How it's tested
- **MySQL** (`go-sqlmock`, regexp matcher): identity put + IsTrustedIdentity
  (table-driven: unknown/match/mismatch/bad-length); session get/put + missing;
  prekey get/remove/markUploaded/count + `GetOrGenPreKeys` generate-missing path;
  app-state version round-trip + missing→zero; sender-key put/get; chat-settings
  muted-forever; NCT salt round-trip; `PutManySessions` txn commit + rollback;
  device upsert (full arg vector) + `ErrDeviceIDMustBeSet` + delete + post-put
  store wiring; LID put (incl. cache short-circuit on second call) + get both
  directions + non-PN rejection.
- **SQLite** (real file modernc DB): `Open` upgrades and serves an unpaired
  device; missing-foreign-keys dsn is rejected.
- **Selector**: mysql requires db; sqlite requires dsn; `sqlite3` alias; unknown
  driver errors; end-to-end sqlite open → GetFirstDevice.

Verify: `CGO_ENABLED=0 go build ./internal/wa/store/... && CGO_ENABLED=0 go test ./internal/wa/store/...`
