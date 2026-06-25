# Queue (asynq background jobs)

Status: implemented (Phase 2). Wiring of concrete consumers is Phase 3.

Package: `internal/queue` · import `github.com/ramaadi/quick-whatsapp-gateway/internal/queue`.

## Scope

Redis-backed background jobs on [hibiken/asynq], covering three masterplan needs:

| Job | Type name | Queue | Masterplan |
|---|---|---|---|
| Async outbound send | `outbox:send` | `outbox` | §8 (async outbox) |
| Webhook delivery + retry | `webhook:deliver` | `webhooks` | §9 (webhook retries) |
| Retention prune | `retention:prune` | `retention` | §5 (daily prune) |

The package owns: typed task constructors + JSON payloads, an enqueue `Client`,
a worker `Server`/mux, and a `REDIS_URL` parser. It performs **no** real work
itself — handlers delegate to consumer interfaces wired in Phase 3.

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
skip enqueueing entirely.

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

### Server (`server.go`)

`NewServer(redisOpt, ServerConfig{Concurrency, Queues}, handlers) *Server`. Wraps
`asynq.Server` pre-wired with `handlers.Mux()`. Default queue weights favour
outbox (6) and webhooks (3) over the once-a-day retention prune (1). Lifecycle:
`Run()` (blocking), `Start()`/`Shutdown()` (graceful).

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

## What Phase 3 must wire

Provide concrete implementations of `OutboxProcessor` (outbound pipeline),
`WebhookDeliverer` (webhooks dispatcher), `RetentionPruner` (store), build a
`Handlers`, construct `Client`/`Server` from `ParseRedisURL(cfg.RedisURL)`, run
the `Server`, and register a periodic `retention:prune` (asynq Scheduler /
PeriodicTaskManager) using `RetentionCutoffMs(time.Now(), cfg.RetentionDays)`.

[hibiken/asynq]: https://github.com/hibiken/asynq
