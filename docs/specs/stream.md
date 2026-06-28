# Stream (event fan-out + transport-agnostic Pump)

Status: implemented (`internal/stream`).

Transport half of eventing (masterplan §11). The event **envelope + catalog + ignore
rules** are shared and documented in [`eventing.md`](./eventing.md); this file covers
only live delivery: how events are fanned out over Redis pub/sub and pumped to a
client connection.

The **client transport is the central router's WebSocket** (`GET /api/v1/realtime`,
ticket-redeemed — see [`router.md`](./router.md)). The gateway's legacy NDJSON
`GET /api/v1/events` transport has been **removed**: a serverless frontend cannot hold
a long-lived streaming connection to N gateways, and a browser WebSocket cannot carry
an `Authorization` header, so realtime is terminated once on the router. The gateway's
only role in realtime now is to **publish** events to Redis; the router subscribes and
serves clients.

## Scope

1. **Publisher** — the `EventSink` the inbound pipeline writes `domain.Event`s to. It
   marshals each event to its canonical JSON envelope and `PUBLISH`es it to a
   per-`(organization, session)` Redis channel. This still runs **in the gateway**.
2. **Pump** — the transport-agnostic `subscribe → connected → replay → tail` loop. It
   subscribes per resolved `Scope`, emits a `connected` frame, replays `event_log`
   entries on `?since=` (org/session scopes only — firehose is live-tail), tails live
   events with a `ping` heartbeat, and writes each framed JSON message to a `Sink`.
   The **router** owns the only `Sink` today (a WebSocket text-frame writer).
3. **Live registry** — `ConnRegistry`, a registry of open connections keyed by the
   authenticating principal so the control bus (§4.6) can **close live connections on
   revocation** (api-key revoked, user banned, org membership removed). The router
   wires this; the control-bus subscriber lives on the router.

Each message the Pump emits is exactly one JSON value (a `connected`/`ping` frame or a
full event envelope). The Sink decides the wire framing — the router writes one
WebSocket text frame per message.

## Channel naming

```
evt:<organization>:<session>      # one event is published here
evt:<organization>:*              # organization-wide subscriber pattern (all sessions)
evt:*                             # firehose pattern (admin; all orgs/sessions)
```

Organization id comes **first** so an org glob (`evt:<organization>:*`) can never match
another org's channel — pub/sub is the isolation boundary. A single-session subscriber
uses the exact channel; an all-sessions subscriber uses `PSubscribe` on the org pattern;
the admin firehose uses `PSubscribe` on `evt:*`.

## Key types / interfaces

All collaborators are **consumer interfaces** defined in this package (no sibling
imports); the composition root wires concrete types in.

- `RedisClient` — `Publish` / `Subscribe` / `PSubscribe` over the **work** Redis
  (`REDIS_URL`, fan-out). `*redis.Client` satisfies it directly.
- `EventLogReader` — `ListSince(ctx, organization, session, afterEventID, limit) ([]domain.EventLogEntry, error)`.
  Returns entries strictly **after** `afterEventID`, ascending cursor order, capped at
  `limit`. `session==""` = all sessions. The MySQL implementation lives in `store`.
- `Sink` — `Send(ctx, payload []byte) error`. The transport adapter; the router's
  WebSocket writer is the only implementation.
- `Scope` — the resolved subscription: firehose (`Organization==""`), organization
  (`Organization` set, `Session==""`), or single session (both set).
- `ConnRegistry` + `ConnIdentity` — track each open connection by
  `(keyId, userId, organizationId)` so the control-bus subscriber can drop the matching
  connections (see §4.6).
- `Clock` / `Ticker` — injectable so the ~20s heartbeat is testable with a fake ticker.

Constructors: `NewPublisher(RedisClient, *slog.Logger)`, `NewPump(PumpConfig)`, and
`NewConnRegistry()`. `PumpConfig` requires `Redis`; `LogReader`, `Clock`, `Heartbeat`
(seconds), and `Log` are optional with sane defaults.

## Pump lifecycle (`Pump.Run`)

1. **Parse filter** — the `events` allow-list (`*`/empty = all, else comma-list). A
   filter that resolves to nothing → error.
2. **Subscribe first** — open the Redis subscription *before* any replay, so events
   published during replay are not lost in the gap between "end of replay" and "start
   of tail".
3. **Connected frame** — emit `{"event":"connected","timestamp":…,"heartbeatSeconds":N}`
   first, so the client confirms the connection is live at once (otherwise it waits up
   to one heartbeat interval for the first byte) and learns the heartbeat cadence to
   size its own dead-stream timeout.
4. **Replay** (only if `since` is set, a `LogReader` is wired, and the scope is
   org/session) — stream matching `event_log` entries oldest-first, capped at
   `resumeReplayLimit` (1000). The id of the last entry read is remembered as the
   **boundary**.
5. **Tail** — copy live events from the subscription; write the raw published bytes as
   one message. The boundary id (if any) is skipped exactly once to dedup the
   replay/tail overlap. A `ping` is emitted on every heartbeat tick. The loop exits on
   context cancel (client disconnect / shutdown / control-bus drop), subscription
   close, or a `Sink` error (client gone).

Replayed messages are rebuilt into the **same envelope shape** as live ones (`schema`,
`id`, `event`, `session`, `organization`, `timestamp`, `payload`) so a consumer cannot
tell a replayed event from a live one.

## Resume after a 5-min JWT (the seamless reconnect, §4.7)

A dashboard JWT expires in ~5 min, but the realtime authz happens **only at ticket
mint**. The client refreshes its JWT, mints a new ticket with `since={lastEventId}`,
and reopens the WebSocket; the router's Pump replays from `event_log` then tails, so the
consumer's view is never torn. A control-bus drop forces the same reconnect, which
re-validates and fails closed if access is gone.

## Decisions

- **Fan-out only in the Publisher.** Durable `event_log` persistence (for `since`) is
  owned by the inbound fan-out stage; the Publisher never blocks the write path on it.
- **Publish raw bytes on the tail.** Live events are forwarded as the exact published
  payload (after a cheap `id`+`event` peek for filtering/dedup) — no re-marshal.
- **Boundary dedup over `since`.** Because we subscribe before replaying, the event that
  `since` resumes *at* may arrive on both paths; the tail drops the single message whose
  id equals the last replayed id.
- **Empty organization is a hard error** in `Publish` — an event with no owning org is
  unaddressable.
- **One transport.** Realtime is WebSocket-only on the router; the gateway publishes and
  the router pumps. The `Pump`/`Sink` seam keeps the loop transport-agnostic should
  another transport ever be added.

## How it's tested

`go test ./internal/stream/...` (also clean under `-race`), miniredis for real pub/sub
behind a real `*redis.Client`, fake `Clock`/`Ticker` and fake `EventLogReader`:

- **Publisher** — events publish to the correct `(organization, session)` channel with
  the full envelope (incl. `organization`) + nested payload intact; empty org errors.
- **Filtering** — `parseEventFilter` table tests.
- **Registry** — `ConnRegistry` registers/deregisters and drops matching connections by
  `keyId` / `userId` / `(userId, organizationId)`.

(The Pump's replay/tail/heartbeat behavior is exercised end-to-end through the router's
realtime tests in `internal/router`.)

## What the composition root wires

- **Gateway:** register `*Publisher` as the system's `EventSink` so inbound fan-out
  reaches Redis. The gateway wires nothing else from this package.
- **Router:** a `RedisClient` (the shared work `*redis.Client`, `REDIS_URL`), an
  `EventLogReader` backed by the `event_log` MySQL repo (`store`) for `since` replay, a
  `*Pump`, a WebSocket `Sink`, and the live `ConnRegistry` into the control-bus
  subscriber so `ctrl:*` revocations drop matching connections.
