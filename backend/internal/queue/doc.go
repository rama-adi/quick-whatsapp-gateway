// Package queue defines the asynq-backed retention-prune job (masterplan §5:
// daily prune of old data). Outbound sends and webhook deliveries are not
// asynq jobs: the API's durable MySQL work tables own them.
//
// It owns the typed task constructors + JSON payloads, a thin Client wrapper for
// enqueueing, a Server/mux wrapper for processing, and a REDIS_URL parser. The
// actual work is delegated to the RetentionPruner consumer interface defined here and wired to concrete types by
// the composition root — this package imports no sibling internal packages.
package queue
