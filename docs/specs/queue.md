# Queue (asynq background jobs)

Status: implemented.

Package: `internal/queue` · import `github.com/ramaadi/quick-whatsapp-gateway/internal/queue`.

## Scope

Redis-backed background jobs on [hibiken/asynq], covering three masterplan needs.
The gateway process owns both the worker and retention scheduler; there is no
separate cron service to deploy.

| Job | Type name | Queue | Masterplan |
|---|---|---|---|
| Async outbound send | `outbox:send` | `outbox` | §10 (async outbox) |
| Webhook delivery + retry | `webhook:deliver` | `webhooks` | §11 (webhook retries) |
| Retention prune | `retention:prune` | `retention` | §7 (daily prune) |

The package owns: typed task constructors + JSON payloads, an enqueue `Client`,
a worker `Server`/mux, and a `REDIS_URL` parser. It performs **no** real work
itself — handlers delegate to consumer interfaces wired in Phase 3.

## Two Redis roles (§4.6)

The queue runs entirely on the **work** Redis. v2 splits Redis into two roles,
collapsible to one instance:

| Role | Env | Carries | Who connects |
|---|---|---|---|
| **Work** | `REDIS_URL` | asynq queue (this package), rate-limit buckets, idempotency, NDJSON stream fan-out | gateways (+ the router for `wa:rl:*` edge rate-limit) |
| **Control bus** | `PUBSUB_REDIS_URL` (defaults to `REDIS_URL`) | low-volume `ctrl:*` pub/sub — `ctrl:apikey.revoked` / `ctrl:user.banned` / `ctrl:member.removed` | frontend (publish) + the **router** (subscribe) |

> **Central-router (Increment A):** the `ctrl:*` **subscriber is the router now**, not the gateways
> (it owns end-user authn + the api-key positive cache). The gateways no longer subscribe to the
> control bus or hold a key cache. One Redis is still the default (`PUBSUB_REDIS_URL` falls back to
> `REDIS_URL`); the dedicated-bus split remains a later env change, no code change.

- **Single instance (dev / single server):** leave `PUBSUB_REDIS_URL` unset → it
  falls back to `REDIS_URL`; one Redis does everything.
- **Split (prod / multi-gateway):** point `PUBSUB_REDIS_URL` at a shared, possibly
  managed Redis reachable by the frontend and every gateway; keep the high-volume
  work Redis local to each gateway. The control bus is the frontend's **only** Redis
  dependency (publish-only).

**Key/channel prefixes** (namespacing, not DB numbers — managed Redis often disallows
`SELECT`):

- work keys → `gw:…` (per-gateway state under `gw:{GATEWAY_ID}:…`); asynq keeps its
  own `asynq:` prefix; the rate limiter uses `wa:rl:…` (outbound-pipeline.md).
- stream fan-out channels → `evt:{organization}:{session}` (stream.md).
- control-bus channels → `ctrl:apikey.revoked`, `ctrl:user.banned`, `ctrl:member.removed`.
- `REDIS_PREFIX` namespaces application-owned keys/channels, including the
  retention claim, but not Asynq's fixed queue keys. Independent stacks need
  separate Redis databases.

> The control-bus **subscriber** lives in `internal/controlbus`, not this package
> (asynq is work-queue only). With the central router (Increment A) it runs **on the
> router**: it evicts the **api-key cache** (`internal/authz`, a ~60s positive cache
> keyed by SHA-256 of the raw key, indexed by keyId/userId/orgId) on revocation. The
> live **stream-drop** on revocation lands with the realtime WebSocket endpoint in
> Increment B. Redis pub/sub is fire-and-forget; the 60s cache TTL covers any `ctrl:*`
> message missed while the router was down.

## Key types

### Tasks & payloads (`tasks.go`)

Task type names are stable wire constants (`TypeOutboxSend`, `TypeWebhookDeliver`,
`TypeRetentionPrune`). Payloads are minimal — they carry an **id**, not a snapshot,
so the queued blob can never drift from the persisted row:

- `OutboxSendPayload{ outboxId string }`
- `WebhookDeliverPayload{ deliveryId uint64 }`
- `RetentionPrunePayload{ cutoffMs int64 }` (epoch-ms; rows older than this go)

Constructors `NewOutboxSendTask(id, opts…)`, `NewWebhookDeliverTask(id, opts…)`,
`NewRetentionPruneTask(cutoffMs, opts…)` return `*asynq.Task`; callers append
`asynq.Option`s (MaxRetry, ProcessIn, TaskID/Unique for dedup, …).

### Client (`client.go`)

`NewClient(redisOpt asynq.RedisClientOpt) *Client` wraps `asynq.Client`. Typed
helpers `EnqueueOutboxSend`, `EnqueueWebhookDeliver`, `EnqueueRetentionPrune` take
a `context.Context` first arg, default-route to the right queue (caller opts win,
last-option-wins), and return `*asynq.TaskInfo`. `Close()` releases the conn.

`RetentionCutoffMs(now, retentionDays)` computes the prune cutoff and returns
`ok=false` when `retentionDays <= 0` (§5: 0 = keep forever) so the scheduler can
skip enqueueing entirely. `RETENTION_DAYS` is a gateway configuration value:
negative values are rejected at boot. The daily gateway scheduler uses a UTC
midnight cutoff, retaining at least the configured number of full UTC days (and
the current partial day).

### Consumer interfaces + handlers (`handlers.go`)

Defined here (consumer-defined interfaces), implemented in Phase 3:

```go
type OutboxProcessor interface { ProcessOutbox(ctx, outboxID string) error }
type WebhookDeliverer interface { DeliverWebhook(ctx, deliveryID uint64) error }
type RetentionPruner  interface { Prune(ctx, cutoffMs int64) error }
```

`Handlers{ Outbox, Webhooks, Retention }` bundles them; `Handlers.Mux()` builds an
`*asynq.ServeMux`, registering a handler **only** for each non-nil consumer.
Handlers are thin: decode → validate → delegate. A malformed/invalid payload is
wrapped with `asynq.SkipRetry` (never succeeds on retry); a consumer error is
returned plain so asynq retries per the task's MaxRetry.

### Server and retention scheduling (`server.go`)

`NewServer(redisOpt, ServerConfig{Concurrency, Queues}, handlers) *Server`. Wraps
`asynq.Server` pre-wired with `handlers.Mux()`. Default queue weights favour
outbox (6) and webhooks (3) over the once-a-day retention prune (1). Lifecycle:
`Run()` (blocking), `Start()`/`Shutdown()` (graceful).

At gateway boot, `cmd/server` starts the worker and the daily retention
scheduler when `RETENTION_DAYS > 0`. All gateway replicas that participate in
this maintenance job must use the same work `REDIS_URL` database **and**
`REDIS_PREFIX`. The scheduled task uses Redis-backed singleton/dedup admission,
so a shared deployment gets one logical prune per cadence, not one destructive
job per gateway replica. Duplicate delivery is still safe: the worker's deletes
are idempotent and batch-bounded. Asynq's general queue keys remain in its
fixed namespace, while retention also includes `REDIS_PREFIX` in its daily
claim marker and task id. Independent stacks therefore need separate Redis
databases; `REDIS_PREFIX` alone does not isolate their Asynq queues. With
`RETENTION_DAYS=0`, the scheduler is not started and no retention task is
enqueued.

The task payload contains the cutoff captured at enqueue time. `RetentionWorker`
deletes in bounded batches and repeats only until that batch is exhausted; it
does not hold a long transaction or monopolize the work queue. Failures are
returned to Asynq for retry. Retention is deliberately low priority, so routine
maintenance cannot starve outbound sends or webhook work.

### REDIS_URL parsing (`redis.go`)

`ParseRedisURL(raw) (asynq.RedisClientOpt, error)`. Rolled by hand rather than
using `asynq.ParseRedisURI` because the latter ignores the **username** (Redis 6+
ACL) component. Supports `redis://` and `rediss://` (TLS, ServerName = host),
optional ACL username + password, default port 6379, and a db index from either
the path (`redis://h/2`) or `?db=` query. Invalid scheme/host/db → error.

## Decisions

- **Id-only payloads.** Handlers reload the authoritative row; the queue is a
  trigger, not a data store.
- **Consumer interfaces, no sibling imports.** Imports are limited to stdlib,
  asynq, and `internal/domain` (only for shared conventions). Concrete
  store/wa/webhook types are injected in Phase 3.
- **SkipRetry vs retry.** Payload-shape errors skip retry; runtime/consumer
  errors retry. This keeps poison messages out of the retry loop while still
  retrying transient failures (network, locked rows).
- **Separate queues + priority** so async sends aren't starved by a retry storm,
  and retention never blocks live traffic.
- **Retention is conservative.** A positive `RETENTION_DAYS` prunes old
  `messages`, `event_log`, and only terminal (`delivered` / `dead`) webhook
  delivery rows. Pending and retryable-failed webhook rows are never removed by
  retention, even when their creation time is old: they still need their durable
  payload and retry state. Their referenced event-log rows are protected too;
  an old event is deleted only after no pending/failed delivery references it.
  Deleting other event-log rows bounds realtime resume history; a client whose
  `since` cursor predates the retained window resumes from the oldest retained
  event rather than receiving an error or a synthetic gap.

## How it's tested

`CGO_ENABLED=0 go test ./internal/queue/...` (no Redis required):

- Payload marshal/unmarshal round-trips + stable JSON field names; validation
  (empty/zero/negative ids and cutoff) — `tasks_test.go`.
- `ParseRedisURL` table: host-only default port, path/query db index, password
  only, ACL user+password, `rediss` TLS ServerName, and error cases (empty, bad
  scheme, missing host, non-numeric/negative/nested db) — `redis_test.go`.
- Handler dispatch via a fake consumer + `mux.Handler(task).ProcessTask(...)`
  (no running server): delegation with correct args, consumer-error propagation
  is retryable, malformed payload → `SkipRetry` without calling the consumer,
  and unregistered types hit NotFoundHandler. `RetentionCutoffMs` math —
  `handlers_test.go`.
- Retention worker/repository tests cover positive/disabled retention, a bounded
  batch loop, terminal-only webhook deletion, and safety under repeated task
  delivery. Scheduler tests cover shared-Redis singleton/dedup admission so
  multiple gateways do not enqueue parallel daily sweeps.

## Production wiring

`cmd/server` provides `OutboxProcessor` and `RetentionPruner`, builds the
handler set, and starts the server from `ParseRedisURL(cfg.RedisURL)`. It owns
the retention scheduler and stops it before graceful worker shutdown. Webhook
delivery is presently driven by the dispatcher cadence described in
[`webhooks.md`](webhooks.md); retention's terminal-row rule ensures that cadence
can never lose pending or retryable deliveries.

[hibiken/asynq]: https://github.com/hibiken/asynq
