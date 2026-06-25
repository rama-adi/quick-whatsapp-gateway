# Stream (NDJSON event transport + Redis fan-out)

Status: implemented (`internal/stream`).

Transport half of eventing (masterplan §9). The event **envelope + catalog + ignore
rules** are shared and documented in [`eventing.md`](./eventing.md); this file covers
only the live delivery transport: how events are fanned out over Redis pub/sub and
streamed to clients as chunked NDJSON.

## Scope

1. **Publisher** — the `EventSink` the inbound pipeline writes `domain.Event`s to. It
   marshals each event to its canonical JSON envelope and `PUBLISH`es it to a
   per-`(tenant, session)` Redis channel.
2. **Handler** — `GET /api/v1/events?session={id}&events=*&since={eventId}` served as
   `application/x-ndjson`, chunked, one JSON object per line, with a `ping` heartbeat,
   `?since=` resume, event-type/session filtering, and clean client-disconnect
   teardown.

This is **NDJSON, not SSE** — no `text/event-stream`, no `data:` framing. Each line is
exactly one JSON value terminated by `\n`.

## Channel naming

```
evt:<tenant>:<session>      # one event is published here
evt:<tenant>:*              # tenant-wide subscriber pattern (all sessions)
```

Tenant id comes **first** so a tenant glob (`evt:<tenant>:*`) can never match another
tenant's channel — pub/sub is the isolation boundary. A single-session subscriber uses
the exact channel; an all-sessions subscriber uses `PSubscribe` on the tenant pattern.

## Key types / interfaces

All collaborators are **consumer interfaces** defined in this package (no sibling
imports); Phase 3 wires concrete types in.

- `RedisClient` — `Publish` / `Subscribe` / `PSubscribe`. `*redis.Client` (already a
  dependency) satisfies it directly.
- `EventLogReader` — `ListSince(ctx, tenant, session, afterEventID, limit) ([]domain.EventLogEntry, error)`.
  Returns entries strictly **after** `afterEventID`, ascending cursor order, capped at
  `limit`. `session==""` = all sessions. The MySQL implementation lives in `store`.
- `TenantAccessor` — `TenantFromContext(ctx) (tenantID, ok)`. Auth is applied upstream
  (Phase 3 middleware); the handler only reads the authenticated tenant. A
  `TenantAccessorFunc` adapter is provided.
- `Clock` / `Ticker` — injectable so the ~20s heartbeat is testable with a fake ticker.
  `SystemClock` (wrapping `time.NewTicker`) is the default.

Constructors: `NewPublisher(RedisClient, *slog.Logger)` and `NewHandler(HandlerConfig)`.
`HandlerConfig` requires `Redis` + `Tenant`; `LogReader`, `Clock`, `Heartbeat` (seconds),
and `Log` are optional with sane defaults.

## Request lifecycle (`Handler.ServeHTTP`)

1. **Authorize** — tenant from context; `401` (JSON error envelope) if absent.
2. **Parse** `session`, `events` (`*`/empty = all, else comma-list), `since`. An
   `events=` value that resolves to nothing → `400`.
3. **Subscribe first** — open the Redis subscription *before* any replay, so events
   published during replay are buffered on the subscription and not lost in the gap
   between "end of replay" and "start of tail".
4. **Write headers + flush** (`application/x-ndjson`, `no-cache`, `X-Accel-Buffering:no`)
   so the client sees the stream open immediately.
5. **Replay** (only if `?since=` and a `LogReader` is wired) — stream matching
   `event_log` entries oldest-first, capped at `resumeReplayLimit` (1000). The id of the
   last entry read is remembered as the **boundary**.
6. **Tail** — copy live events from the subscription; for each, write the raw published
   bytes as one line + flush. The boundary id (if any) is skipped exactly once to dedup
   the replay/tail overlap. A `ping` line is emitted on every heartbeat tick. The loop
   exits on request-context cancel (client disconnect / shutdown) or subscription close.

Replayed lines are rebuilt into the **same envelope shape** as live lines (`schema`,
`id`, `event`, `session`, `tenant`, `timestamp`, `payload`) so a consumer cannot tell a
replayed event from a live one.

## Decisions

- **Fan-out only in the Publisher.** Durable `event_log` persistence (for `?since=`) is
  owned elsewhere; the Publisher never blocks the write path on it and vice versa.
- **Publish raw bytes on the tail.** Live events are forwarded as the exact published
  payload (after a cheap `id`+`event` peek for filtering/dedup) — no re-marshal, so the
  bytes are stable and cheap.
- **Boundary dedup over `since`.** Because we subscribe before replaying, the event that
  `since` resumes *at* may arrive on both paths; the tail drops the single line whose id
  equals the last replayed id.
- **Empty tenant is a hard error** in `Publish` — an event with no tenant is
  unaddressable.
- **Heartbeat via injected `Clock`** — production uses `time.NewTicker`; tests drive a
  fake ticker for deterministic assertions.

## How it's tested

`go test ./internal/stream/...` (also clean under `-race`), miniredis for real pub/sub
behind a real `*redis.Client`, `httptest` servers for true chunked/flushed streaming,
fake `Clock`/`Ticker` and fake `EventLogReader`:

- **NDJSON encoding** — one valid JSON object per line, `\n`-terminated, correct
  `Content-Type`, envelope fields + nested payload intact.
- **Filtering** — `parseEventFilter` table tests (`*`/empty/comma-list/blank-token/empty)
  and an end-to-end test that a non-matching type is dropped while a matching one passes.
- **Session filter** — exact-channel subscription delivers only the targeted session.
- **Heartbeat** — a manual tick produces a `{"event":"ping","timestamp":…}` line.
- **`?since=` resume** — log entries replay in order (with correct `ListSince` args),
  then live events tail; a separate test asserts the boundary duplicate is deduped.
- **Replay error** — a `ListSince` failure after headers are sent emits a final
  `{"event":"error"}` line and stops.
- **Client cancel** — cancelling the request context tears the stream down.

## What Phase 3 must wire

- A `RedisClient` (the shared `*redis.Client`).
- A `TenantAccessor` reading the tenant id the auth/api-key middleware put on the
  context (mount the handler behind that middleware on `GET /api/v1/events`).
- An `EventLogReader` backed by the `event_log` MySQL repo (`store`) for `?since=`.
- Register `*Publisher` as the system's `EventSink` so inbound fan-out reaches the
  stream.
