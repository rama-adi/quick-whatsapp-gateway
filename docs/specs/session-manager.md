# Session Manager

Status: implemented (M2). Package `internal/wa`, files `manager.go`, `session.go`.

## Scope

Owns the in-process WhatsApp connection layer (§3, §6): a `*whatsmeow.Client`
per attached number, each holding a live WebSocket. Responsibilities:

- Load devices from the whatsmeow keystore on boot and adopt the matching
  `wa_sessions` rows; resume sessions that were meant to be running.
- Lifecycle: create / start / stop / restart / logout.
- Reconnect with exponential backoff + full jitter.
- Status state machine `STARTING · SCAN_QR_CODE · WORKING · FAILED · STOPPED ·
  LOGGED_OUT`, emitting `session.status` on change.
- Pairing: QR (`GetQRChannel` before `Connect`, stream codes as `auth.qr`) and
  pairing-code (`PairPhone`, emit `auth.code`).
- Terminal events (`LoggedOut` / `StreamReplaced` / `TemporaryBan` /
  `ClientOutdated` / fatal `ConnectFailure`) → mark `LOGGED_OUT`/`FAILED`, STOP
  reconnect, emit status.
- Admin-number bootstrap (§6): if `WHATSAPP_ADMIN_NUMBER` is set and no keystore
  device exists for it, create an `is_admin_session` row and surface the pairing
  code (returned from `Boot` + logged to console + emitted as `auth.code`).
- Register one whatsmeow event handler per session that forwards EVERY event to
  the injected inbound handler (tagged session/tenant/isAdmin).

## Key types / interfaces

Consumer interfaces (defined here; Phase 3 wires concrete types — no sibling
internal imports):

- `Keystore` — slice of the whatsmeow device container
  (`GetAllDevices/GetFirstDevice/GetDevice/NewDevice/DeleteDevice`). Satisfied by
  `*sqlstore.Container` and the hand-built MySQL container. Uses the external
  `store.Device` type, which is allowed.
- `SessionRepo` — the `wa_sessions` methods called:
  `Get/GetByJID/ListByTenant/Create/Update/UpdateStatus`.
- `EventSink` — `Publish(ctx, domain.Event)`; the manager only emits
  `session.status`, `auth.qr`, `auth.code`.
- `InboundHandler` — `Handle(ctx, sessionID, tenantID, isAdmin, evt any)`; every
  whatsmeow event is forwarded here.
- `Clock` — `NowMs()`; defaults to `domain.NowMs()`.
- `waClient` (unexported) — the narrow slice of `*whatsmeow.Client` the session
  drives (`Connect/Disconnect/IsConnected/IsLoggedIn/Logout/AddEventHandler/
  GetQRChannel/PairPhone`). The real client satisfies it (compile-time assert);
  tests inject a fake, so the full lifecycle is exercised without a WebSocket.
  Built via a swappable `clientFactory` (`SetClientFactory`).

Core types:

- `Manager` — `map[sessionID]*ManagedSession` under `sync.RWMutex`; constructor
  `NewManager(keystore, repo, sink, inbound, clock, log, cfg)`.
- `ManagedSession` — wraps the device + client + status + reconnect bookkeeping +
  per-session `context.CancelFunc`. All mutable state guarded by its own mutex.
- `Config` — admin number/tenant, rate/auto-read defaults, optional backoff.

## Decisions

- **Pure cores for testability.** Three pure functions carry the load-bearing
  logic and are unit-tested directly:
  - `classifyEvent(evt any) transition` — maps a whatsmeow event to
    `(status, changed, terminal, keepReconnect)`. `Connected→WORKING`,
    `PairSuccess→STARTING`, terminal events→`FAILED`/`LOGGED_OUT` with reconnect
    stopped; `Disconnected` and transient `ConnectFailure` cause no status change
    (the reconnect loop retries).
  - `isFatalConnectFailure(reason)` — distinguishes permanent
    (logged-out/banned/locked/outdated/bad-UA) from transient connect failures.
  - `backoffFor(cfg, attempt, *rand.Rand)` — exponential `base*factor^n` clamped
    to `max`, then **full jitter** (`uniform[0, ceiling]`). Deterministic given a
    seeded `*rand.Rand`. Production default: 1s base, ×2, cap 2m.
  - `adminNeedsPairing(number, deviceJIDs, adminJID)` — the bootstrap decision.
- **Per-session RNG** seeded from `crypto/rand` so concurrent sessions don't
  share a jitter schedule (avoids thundering-herd reconnects). `math/rand` is
  correct here — jitter needs non-correlation, not cryptographic strength.
- **Status ownership is single-writer.** `teardown` clears runtime state but does
  NOT touch status; `setStatus` is the sole owner of the status field, its
  persistence (`UpdateStatus`), and its `session.status` emission, and it
  **dedups** (no event when the status is unchanged). This keeps teardown from
  pre-setting a status that would suppress the subsequent emit.
- **Goroutine lifecycle.** Each running session gets a context derived via
  `context.WithoutCancel(parent)` (detached from the request) + a `CancelFunc`.
  Stop/Logout/Shutdown cancel it; the reconnect loop and QR pump select on
  `ctx.Done()` and exit cleanly. The loop polls connection state on a 500ms
  ticker rather than threading extra channels through whatsmeow's event model.
- **Boot resume policy** (`shouldResume`): `STOPPED`/`LOGGED_OUT`/`FAILED` stay
  down; anything that was live or mid-startup resumes.
- **Pairing display name** is `"Chrome (Linux)"` (whatsmeow validates the
  `"Browser (OS)"` format and 400s otherwise).
- **whatsmeow is imported directly** (external module): `whatsmeow`, `/store`,
  `/types`, `/types/events`, `/util/log`. No sibling internal packages.

## How it's tested

`session_test.go` + `manager_test.go`, table-driven, all boundaries faked:

- `classifyEvent` across every relevant event incl. fatal vs transient
  `ConnectFailure`; `isFatalConnectFailure` matrix.
- `backoffFor`: determinism (same seed → same schedule), full-jitter bounds
  per attempt, cap never exceeded, negative attempt clamped.
- `adminNeedsPairing` / `deviceJIDs`; `bootstrapAdmin` already-paired (no code),
  needs-pairing (returns code, creates `is_admin_session` row, emits `auth.code`),
  disabled.
- `setStatus` dedup; event handler terminal-stops-reconnect + tears down + emits
  + still forwards to inbound; `Connected` resets backoff; `PairSuccess` records
  the JID/LID onto the session row.
- Lifecycle: `CreateSession` persists + registers + applies rate defaults;
  `Start` rejects unpaired; `Stop` tears down + marks STOPPED; `Logout` calls
  `client.Logout`, deletes the keystore device, marks LOGGED_OUT; not-found
  errors; `Boot` adopts paired devices without resuming a STOPPED session.

Verified: `CGO_ENABLED=0 go build ./internal/wa`, `go test ./internal/wa` (incl.
`-race`), `go vet ./internal/wa` all pass.

## What Phase 3 must wire

- A concrete `Keystore` (`*sqlstore.Container` for SQLite/Postgres, or the MySQL
  container) — `internal/wa/store`.
- A concrete `SessionRepo` over `wa_sessions` — `internal/store`.
- A concrete `EventSink` (Redis pub/sub + webhook enqueue + event_log) —
  `internal/stream` / `internal/webhooks`.
- A concrete `InboundHandler` — `internal/wa/inbound`.
- Populate `Config` from ENV (`WHATSAPP_ADMIN_NUMBER`, `DEFAULT_RATE_*`,
  `DEFAULT_AUTO_READ`) and call `Boot` on startup, surfacing the returned admin
  pairing code; call `Shutdown` on stop.
