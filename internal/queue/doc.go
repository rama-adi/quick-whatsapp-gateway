// Package queue defines asynq-backed background jobs for the gateway:
//
//   - outbox-send: drive a persisted outbox row to WhatsApp (masterplan §8).
//   - webhook-deliver: deliver+retry a webhook delivery row (masterplan §9).
//   - retention-prune: daily prune of old data (masterplan §5).
//
// It owns the typed task constructors + JSON payloads, a thin Client wrapper for
// enqueueing, a Server/mux wrapper for processing, and a REDIS_URL parser. The
// actual work is delegated to consumer interfaces (OutboxProcessor,
// WebhookDeliverer, RetentionPruner) defined here and wired to concrete types in
// Phase 3 — this package imports no sibling internal packages.
package queue
