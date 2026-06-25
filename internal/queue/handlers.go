package queue

import (
	"context"
	"fmt"

	"github.com/hibiken/asynq"
)

// Consumer interfaces (defined here, implemented in Phase 3). The handlers in
// this package are thin: decode the payload, then delegate to one of these. This
// keeps queue free of sibling-package imports and makes handler dispatch trivial
// to test with fakes.

// OutboxProcessor drives a persisted outbox row to WhatsApp. Implementations load
// the row by id, send via the wa client, and update outbox status / wa_message_id.
// Returning a non-nil error makes asynq retry per the task's MaxRetry policy.
type OutboxProcessor interface {
	ProcessOutbox(ctx context.Context, outboxID string) error
}

// WebhookDeliverer performs one delivery attempt for a webhook_deliveries row:
// load delivery+webhook+event, POST with HMAC, record response/attempts, and on
// failure either schedule the next retry or mark dead. A returned error signals
// asynq to retry the task.
type WebhookDeliverer interface {
	DeliverWebhook(ctx context.Context, deliveryID uint64) error
}

// RetentionPruner deletes event_log/messages/webhook_deliveries rows older than
// the given epoch-ms cutoff (§5 daily prune).
type RetentionPruner interface {
	Prune(ctx context.Context, cutoffMs int64) error
}

// Handlers bundles the consumer dependencies the worker handlers delegate to.
// Any field may be nil if that job type is not registered for a given server.
type Handlers struct {
	Outbox    OutboxProcessor
	Webhooks  WebhookDeliverer
	Retention RetentionPruner
}

// handleOutboxSend decodes the task and delegates to the OutboxProcessor.
func (h Handlers) handleOutboxSend(ctx context.Context, t *asynq.Task) error {
	if h.Outbox == nil {
		return fmt.Errorf("queue: no OutboxProcessor registered")
	}
	p, err := parseOutboxSend(t)
	if err != nil {
		// A malformed payload will never succeed on retry; skip retries.
		return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
	}
	if err := h.Outbox.ProcessOutbox(ctx, p.OutboxID); err != nil {
		return fmt.Errorf("process outbox %s: %w", p.OutboxID, err)
	}
	return nil
}

// handleWebhookDeliver decodes the task and delegates to the WebhookDeliverer.
func (h Handlers) handleWebhookDeliver(ctx context.Context, t *asynq.Task) error {
	if h.Webhooks == nil {
		return fmt.Errorf("queue: no WebhookDeliverer registered")
	}
	p, err := parseWebhookDeliver(t)
	if err != nil {
		return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
	}
	if err := h.Webhooks.DeliverWebhook(ctx, p.DeliveryID); err != nil {
		return fmt.Errorf("deliver webhook %d: %w", p.DeliveryID, err)
	}
	return nil
}

// handleRetentionPrune decodes the task and delegates to the RetentionPruner.
func (h Handlers) handleRetentionPrune(ctx context.Context, t *asynq.Task) error {
	if h.Retention == nil {
		return fmt.Errorf("queue: no RetentionPruner registered")
	}
	p, err := parseRetentionPrune(t)
	if err != nil {
		return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
	}
	if err := h.Retention.Prune(ctx, p.CutoffMs); err != nil {
		return fmt.Errorf("retention prune (cutoff=%d): %w", p.CutoffMs, err)
	}
	return nil
}

// Mux builds an *asynq.ServeMux that registers each handler whose corresponding
// consumer is non-nil. Exposed so tests (and Phase 3) can dispatch tasks without
// a running server: mux.Handler(task) resolves the registered HandlerFunc.
func (h Handlers) Mux() *asynq.ServeMux {
	mux := asynq.NewServeMux()
	if h.Outbox != nil {
		mux.HandleFunc(TypeOutboxSend, h.handleOutboxSend)
	}
	if h.Webhooks != nil {
		mux.HandleFunc(TypeWebhookDeliver, h.handleWebhookDeliver)
	}
	if h.Retention != nil {
		mux.HandleFunc(TypeRetentionPrune, h.handleRetentionPrune)
	}
	return mux
}
