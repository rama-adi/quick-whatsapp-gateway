# whatsmeow store

Status: implemented (R2); Increment 3 keystore-safety seams implemented.

> **Target migration, not current runtime (gRPC control-plane Increment 0).** The whatsmeow SQLite
> keystore remains mandatory and gateway-local. Reliable API handoff uses a separate `journal.db` on
> the same persistent volume; application tables must never be added to whatsmeow-owned storage.
> Automated remote keystore backup/rehydration remains deferred. Increment 3 adds local persistent-path
> validation, integrity checks, missing/corrupt states, and checkpoint/close seams. Desired-state and
> control-stream wiring consume those seams separately; this package does not claim that health telemetry
> is already sent on the control stream.

### Initial journal and command-ledger policy (locked defaults)

- Target offline buffering objective: **72 hours**; this is a sizing objective, not a guaranteed RPO.
- Journal cap: **1 GiB** by default; configuration above **25%** of the persistent-volume budget is
  rejected. Health becomes degraded at 70%, optional sync pauses at 80%, and the gateway becomes
  critical/unready at 90%. Core message/receipt events are never silently discarded.
- Stream in-flight bound: at most **256 events or 1 MiB**, whichever is reached first.
- Command-result ledger retention: **7 days**, never shorter than the API retry/idempotency window.
- `internal/gateway/journal` now implements the journal foundation: a separate WAL-mode
  `journal.db`, transactional stable event-ID deduplication, ordered unacknowledged replay,
  monotonic acknowledgement watermark/removal, quick-check on reopen, and WAL checkpoint on close.
  Its metrics expose unacknowledged entry/byte counts, oldest-entry time, acknowledgement watermark,
  and `healthy`/`degraded`/`paused`/`critical` capacity state. At the cap, append fails explicitly;
  it never silently discards a core event. API ingest and stream acknowledgement wiring remain later.
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
| `internal/gateway/journal` | `journal` | Separate gateway-local durable normalized-event handoff log |

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

### Increment 3 safety API

`Open` remains the compatible create-on-first-use wrapper for local development and unassigned
bootstrap. A gateway that has assigned sessions must instead use `sqlitestore.OpenExisting`.
It first verifies an existing non-empty local file with `PRAGMA quick_check`; its errors classify with
`errors.Is(err, sqlitestore.ErrKeystoreMissing)` or `ErrKeystoreCorrupt`. A caller must stop adoption,
not create a replacement or start pairing. `Managed.Health()` exposes only presence, byte size, integrity,
and the last successful check. `Managed.Close()` checkpoints WAL then closes the container; the runtime
must quiesce WhatsApp work first.

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

`WHATSMEOW_STORE_DSN` must be an explicit local absolute
`file:/...` path. Relative/default DSNs, memory forms, and URI authorities are rejected — the
keystore is the gateway's irreplaceable durable state and can never live on ephemeral storage.

## Operator backup and restore

This is an operator-managed recovery procedure, not automatic failover and not an RPO promise.

1. Drain and stop the gateway after quiescing WA work and successfully checkpointing/closing the store.
2. Snapshot or copy the complete stopped persistent keystore volume. Never copy only a live WAL-mode main file.
3. Treat the snapshot as cryptographic material; restore it only while the old gateway remains stopped.
4. Validate the restored file with `OpenExisting` before adoption, then obtain a new fenced assignment epoch.

No snapshot upload, rehydration, or session portability is implemented here.

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
