# Stream (NDJSON event transport + Redis fan-out)

Status: implemented (`internal/stream`).

Transport half of eventing (masterplan §11). The event **envelope + catalog + ignore
rules** are shared and documented in [`eventing.md`](./eventing.md); this file covers
only the live delivery transport: how events are fanned out over Redis pub/sub and
streamed to clients as chunked NDJSON. The stream **stays in the gateway** — the
serverless frontend cannot hold a long-lived streaming connection, so the browser
connects to the gateway directly (§12).

## Scope

1. **Publisher** — the `EventSink` the inbound pipeline writes `domain.Event`s to. It
   marshals each event to its canonical JSON envelope and `PUBLISH`es it to a
   per-`(organization, session)` Redis channel.
2. **Handler** — `GET /api/v1/events?session={id}&events=*&since={eventId}` served as
   `application/x-ndjson`, chunked, one JSON object per line, opening with a
   `connected` frame, then a `ping` heartbeat, `?since=` resume,
   event-type/session filtering, and clean client-disconnect teardown.
3. **Live registry + control-bus drop** — a registry of open streams keyed by the
   authenticating principal so the control bus (§4.6) can **close live streams on
   revocation** (api-key revoked, user banned, org membership removed).

This is **NDJSON, not SSE** — no `text/event-stream`, no `data:` framing. Each line is
exactly one JSON value terminated by `\n`.

## Auth (two acceptors, §4)

The stream authenticates with the **same** middleware as every other API route — there
is no separate stream auth. A connection presents either:

- `Authorization: Bearer <better-auth JWT>` (dashboard / human), or
- `Authorization: Bearer <api-key>` / `x-api-key: <api-key>` (programmatic).

The §4.3 middleware resolves the principal (org + role/permissions) onto the request
context; the route additionally requires the **events** capability (api-key `events`
permission, or any JWT role). The handler reads only the authenticated **organization**
and serves that org's events — pub/sub channels are the isolation boundary.

## Channel naming

```
evt:<organization>:<session>      # one event is published here
evt:<organization>:*              # organization-wide subscriber pattern (all sessions)
```

Organization id comes **first** so an org glob (`evt:<organization>:*`) can never match
another org's channel — pub/sub is the isolation boundary. A single-session subscriber
uses the exact channel; an all-sessions subscriber uses `PSubscribe` on the org pattern.

## Key types / interfaces

All collaborators are **consumer interfaces** defined in this package (no sibling
imports); the composition root wires concrete types in.

- `RedisClient` — `Publish` / `Subscribe` / `PSubscribe` over the **work** Redis
  (`REDIS_URL`, intra-gateway fan-out). `*redis.Client` satisfies it directly.
- `EventLogReader` — `ListSince(ctx, organization, session, afterEventID, limit) ([]domain.EventLogEntry, error)`.
  Returns entries strictly **after** `afterEventID`, ascending cursor order, capped at
  `limit`. `session==""` = all sessions. The MySQL implementation lives in `store`.
- `OrganizationAccessor` — `OrganizationFromContext(ctx) (organizationID, ok)`. Auth is
  applied upstream (§4 middleware); the handler only reads the authenticated org. An
  `OrganizationAccessorFunc` adapter is provided.
- `PrincipalAccessor` + the live **registry** — track each open stream by
  `(keyId, userId, organizationId)` so the control-bus subscriber can drop the matching
  connections (see §4.6 / `internal/controlbus`).
- `Clock` / `Ticker` — injectable so the ~20s heartbeat is testable with a fake ticker.

Constructors: `NewPublisher(RedisClient, *slog.Logger)` and `NewHandler(HandlerConfig)`.
`HandlerConfig` requires `Redis` + `Organization`; `LogReader`, `Clock`, `Heartbeat`
(seconds), `Registry`, and `Log` are optional with sane defaults.

## Request lifecycle (`Handler.ServeHTTP`)

1. **Authorize** — organization from context; `401` (JSON error envelope) if absent.
2. **Parse** `session`, `events` (`*`/empty = all, else comma-list), `since`. An
   `events=` value that resolves to nothing → `400`.
3. **Subscribe first** — open the Redis subscription *before* any replay, so events
   published during replay are not lost in the gap between "end of replay" and "start
   of tail".
4. **Register** the live stream in the registry under the request's principal, so a
   control-bus `ctrl:*` revocation can close it (deregistered on disconnect).
5. **Write headers + flush** (`application/x-ndjson`, `no-cache`, `X-Accel-Buffering:no`)
   so the client sees the stream open immediately.
6. **Connected frame** — emit `{"event":"connected","timestamp":…,"heartbeatSeconds":N}`
   as the first line, before replay/tail, so the client confirms the stream is live at
   once (otherwise it waits up to one heartbeat interval for the first byte) and learns
   the heartbeat cadence to size its own dead-stream timeout.
7. **Replay** (only if `?since=` and a `LogReader` is wired) — stream matching
   `event_log` entries oldest-first, capped at `resumeReplayLimit` (1000). The id of the
   last entry read is remembered as the **boundary**.
8. **Tail** — copy live events from the subscription; write the raw published bytes as
   one line + flush. The boundary id (if any) is skipped exactly once to dedup the
   replay/tail overlap. A `ping` line is emitted on every heartbeat tick. The loop exits
   on request-context cancel (client disconnect / shutdown / control-bus drop) or
   subscription close.

Replayed lines are rebuilt into the **same envelope shape** as live lines (`schema`,
`id`, `event`, `session`, `organization`, `timestamp`, `payload`) so a consumer cannot
tell a replayed event from a live one.

## Resume after a 5-min JWT (the seamless reconnect, §4.7)

A dashboard JWT expires in ~5 min, but a stream authenticates **only at connect**. The
client refreshes its JWT and reconnects, passing `since={lastEventId}`; the handler
replays from `event_log` and tails, so the consumer's view is never torn. A control-bus
drop forces the same reconnect, which then re-validates and `401`s if access is gone.

## Decisions

- **Fan-out only in the Publisher.** Durable `event_log` persistence (for `?since=`) is
  owned by the inbound fan-out stage; the Publisher never blocks the write path on it.
- **Publish raw bytes on the tail.** Live events are forwarded as the exact published
  payload (after a cheap `id`+`event` peek for filtering/dedup) — no re-marshal.
- **Boundary dedup over `since`.** Because we subscribe before replaying, the event that
  `since` resumes *at* may arrive on both paths; the tail drops the single line whose id
  equals the last replayed id.
- **Empty organization is a hard error** in `Publish` — an event with no owning org is
  unaddressable.
- **Control-bus drops are precise.** The registry is indexed by `keyId`, `userId`, and
  `(userId, organizationId)` so `apikey.revoked`, `user.banned`, and `member.removed`
  each close exactly the affected streams (and only that org's, for member-removed).

## How it's tested

`go test ./internal/stream/...` (also clean under `-race`), miniredis for real pub/sub
behind a real `*redis.Client`, `httptest` servers for true chunked/flushed streaming,
fake `Clock`/`Ticker` and fake `EventLogReader`:

- **NDJSON encoding** — one valid JSON object per line, `\n`-terminated, correct
  `Content-Type`, envelope fields (incl. `organization`) + nested payload intact.
- **Filtering** — `parseEventFilter` table tests and an end-to-end drop/pass test.
- **Session filter** — exact-channel subscription delivers only the targeted session.
- **Heartbeat** — a manual tick produces a `{"event":"ping","timestamp":…}` line.
- **`?since=` resume** — log entries replay in order (with correct `ListSince` args),
  then live events tail; a separate test asserts the boundary duplicate is deduped.
- **Replay error** — a `ListSince` failure after headers emits a final
  `{"event":"error"}` line and stops.
- **Client cancel / control-bus drop** — cancelling tears the stream down and the
  registry deregisters it.

## What the composition root wires

- A `RedisClient` (the shared work `*redis.Client`, `REDIS_URL`).
- An `OrganizationAccessor` reading the org the §4 middleware put on the context (mount
  the handler behind that middleware on `GET /api/v1/events`).
- An `EventLogReader` backed by the `event_log` MySQL repo (`store`) for `?since=`.
- The live `Registry` into the control-bus subscriber (`internal/controlbus`) so
  `ctrl:*` revocations drop matching streams.
- Register `*Publisher` as the system's `EventSink` so inbound fan-out reaches the stream.
