# whatsmeow store

Status: implemented (R2).

> **Target migration, not current runtime (gRPC control-plane Increment 0).** The whatsmeow SQLite
> keystore remains mandatory and gateway-local. Reliable API handoff uses a separate `journal.db` on
> the same persistent volume; application tables must never be added to whatsmeow-owned storage.
> Automated remote keystore backup/rehydration remains deferred. Later increments add persistent-path
> validation, integrity checks, missing/corrupt states, WAL checkpointing, telemetry, assignment
> fencing, and operator recovery guidance; these safeguards are not implemented yet.

### Initial journal and command-ledger policy (locked defaults; implementation follows)

- Target offline buffering objective: **72 hours**; this is a sizing objective, not a guaranteed RPO.
- Journal cap: **1 GiB** by default; configuration above **25%** of the persistent-volume budget is
  rejected. Health becomes degraded at 70%, optional sync pauses at 80%, and the gateway becomes
  critical/unready at 90%. Core message/receipt events are never silently discarded.
- Stream in-flight bound: at most **256 events or 1 MiB**, whichever is reached first.
- Command-result ledger retention: **7 days**, never shorter than the API retry/idempotency window.
- All values remain configurable. Increment 5 soak testing tunes them using observed p50/p95 event
  sizes and peak event rate; per-deployment volume caps and final retention remain configurable.

The whatsmeow **device keystore** — device identities, Signal sessions, prekeys, sender keys,
app-state, the LID map. In v2 it is **SQLite** via whatsmeow's own `sqlstore`, gateway-local on a
persistent volume. The v1 hand-written **MySQL** store (`internal/wa/store/mysql`, the
`wmstore_*` tables, the driver selector) is **retired** — there is no longer any whatsmeow device
state in MySQL. Masterplan §6.1.

## Why SQLite (the change from v1)

v1 ran a custom MySQL whatsmeow store because `go.mau.fi/whatsmeow/store/sqlstore` only knows the
Postgres and SQLite dialects (no MySQL path), and the design wanted device state in the primary
DB. v2 drops that constraint: the keystore is **gateway-local**, so it uses the better-trodden
**`sqlstore`** path natively with **zero custom adapter**. A **pure-Go** driver
(`modernc.org/sqlite`, registered as `sqlite`) keeps the gateway building with `CGO_ENABLED=0` and
shipping as a small static image — no C compiler, no `mattn/go-sqlite3`.

## Package

| Path | Package | Role |
|---|---|---|
| `internal/wa/store/sqlite` | `sqlitestore` | Thin wrapper over `sqlstore` + modernc SQLite |
| `internal/wa/store/store.go` | `wastore` | `Keystore` interface the session manager depends on |

`sqlstore` owns and auto-migrates its own schema inside the SQLite file (the `whatsmeow_*` tables)
— there is **no** `wmstore_*` migration in `migrations/` anymore (those are golang-migrate, MySQL
app-data only; see [`store.md`](store.md)).

### `sqlitestore.Open`

```go
func Open(ctx, dsn string, log waLog.Logger) (*sqlstore.Container, error)
```

modernc registers its driver as `"sqlite"`, and `dbutil.ParseDialect` accepts any `sqlite*`
prefix, so this is `sql.Open("sqlite", dsn)` → `sqlstore.NewWithDB(db, "sqlite", log)` →
`Upgrade`. `Upgrade` requires foreign keys, so the DSN must enable them.

### `wastore.Keystore` (consumer interface)

The Session Manager depends only on this; `*sqlstore.Container` satisfies it:

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

## DSN & persistence

```
WHATSMEOW_STORE_DSN=file:/data/keystore/store.db?_pragma=foreign_keys(on)&_pragma=journal_mode(WAL)
```

The SQLite file holds **device crypto material** — lose it and every number must re-pair. Mount
`/data/keystore` on a **named Docker volume** (`keystore_data`, see [packaging §15] and
`deploy/`). Back it up. In dev, `air` points the DSN at a local path.

## Gateway-local → session pinning

Because the keystore is gateway-local, a WhatsApp session physically lives in exactly one
gateway's SQLite file. That is what **pins a session to a gateway**: `wa_sessions.gateway_id`
records which gateway holds it, and the `gateways` registry carries one self-row in v2 (sharding
across gateways is forward-compatible, not built — masterplan §4.5). Schema in [`store.md`](store.md).

## Boot orphan-guard

On boot, before resuming each device from the keystore, the Session Manager
(`internal/wa/manager.go`) checks the session's owning organization still exists and is enabled in
MySQL, and **skips + marks `STOPPED`** any whose org was deleted/disabled while the gateway was
down. The admin number (`WHATSAPP_ADMIN_NUMBER`) is (re-)provisioned against the SQLite keystore
on boot if no valid device exists for it. Detail: [`trust-model.md`](trust-model.md) § boot
reconciliation.

## How it's tested

`store_test.go` (real modernc SQLite file): `Open` upgrades and serves an unpaired device;
a DSN without foreign keys is rejected; end-to-end open → `GetFirstDevice`.

Verify: `CGO_ENABLED=0 go build ./internal/wa/store/... && CGO_ENABLED=0 go test ./internal/wa/store/...`.
