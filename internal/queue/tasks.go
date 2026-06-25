package queue

import (
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
)

// Task type names. asynq dispatches on these strings, so they double as the
// stable wire identifiers for a job kind. Namespaced to avoid collisions if this
// Redis instance is ever shared.
const (
	TypeOutboxSend     = "outbox:send"
	TypeWebhookDeliver = "webhook:deliver"
	TypeRetentionPrune = "retention:prune"
)

// Queue names. asynq routes tasks to named queues which the Server processes
// with configurable priority. Keeping outbox sends and webhook deliveries on
// separate queues lets Phase 3 tune their relative weight independently.
const (
	QueueOutbox    = "outbox"
	QueueWebhooks  = "webhooks"
	QueueRetention = "retention"
)

// OutboxSendPayload is the JSON body of a TypeOutboxSend task. It carries only
// the outbox row id; the handler loads the full row (payload, session, tenant)
// from the store so the queued blob can't drift from the persisted truth.
type OutboxSendPayload struct {
	OutboxID string `json:"outboxId"`
}

// WebhookDeliverPayload is the JSON body of a TypeWebhookDeliver task. It carries
// the webhook_deliveries row id; the handler loads delivery + webhook + event
// from the store.
type WebhookDeliverPayload struct {
	DeliveryID uint64 `json:"deliveryId"`
}

// RetentionPrunePayload is the JSON body of a TypeRetentionPrune task. CutoffMs
// is an epoch-ms (domain.NowMs) lower bound: rows older than this are deleted.
// Computed by the enqueuer from RETENTION_DAYS so the worker stays policy-free.
type RetentionPrunePayload struct {
	CutoffMs int64 `json:"cutoffMs"`
}

// NewOutboxSendTask builds a TypeOutboxSend task for the given outbox id. Extra
// asynq.Options (Queue, MaxRetry, ProcessIn, Unique, …) may be appended by the
// caller; the constructor sets none so enqueue policy lives at the call site.
func NewOutboxSendTask(outboxID string, opts ...asynq.Option) (*asynq.Task, error) {
	b, err := json.Marshal(OutboxSendPayload{OutboxID: outboxID})
	if err != nil {
		return nil, fmt.Errorf("marshal outbox-send payload: %w", err)
	}
	return asynq.NewTask(TypeOutboxSend, b, opts...), nil
}

// NewWebhookDeliverTask builds a TypeWebhookDeliver task for the given delivery id.
func NewWebhookDeliverTask(deliveryID uint64, opts ...asynq.Option) (*asynq.Task, error) {
	b, err := json.Marshal(WebhookDeliverPayload{DeliveryID: deliveryID})
	if err != nil {
		return nil, fmt.Errorf("marshal webhook-deliver payload: %w", err)
	}
	return asynq.NewTask(TypeWebhookDeliver, b, opts...), nil
}

// NewRetentionPruneTask builds a TypeRetentionPrune task with the given epoch-ms
// cutoff (rows older than cutoff are deleted).
func NewRetentionPruneTask(cutoffMs int64, opts ...asynq.Option) (*asynq.Task, error) {
	b, err := json.Marshal(RetentionPrunePayload{CutoffMs: cutoffMs})
	if err != nil {
		return nil, fmt.Errorf("marshal retention-prune payload: %w", err)
	}
	return asynq.NewTask(TypeRetentionPrune, b, opts...), nil
}

// parseOutboxSend decodes a task payload into OutboxSendPayload.
func parseOutboxSend(t *asynq.Task) (OutboxSendPayload, error) {
	var p OutboxSendPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return p, fmt.Errorf("unmarshal outbox-send payload: %w", err)
	}
	if p.OutboxID == "" {
		return p, fmt.Errorf("outbox-send payload: empty outboxId")
	}
	return p, nil
}

// parseWebhookDeliver decodes a task payload into WebhookDeliverPayload.
func parseWebhookDeliver(t *asynq.Task) (WebhookDeliverPayload, error) {
	var p WebhookDeliverPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return p, fmt.Errorf("unmarshal webhook-deliver payload: %w", err)
	}
	if p.DeliveryID == 0 {
		return p, fmt.Errorf("webhook-deliver payload: zero deliveryId")
	}
	return p, nil
}

// parseRetentionPrune decodes a task payload into RetentionPrunePayload.
func parseRetentionPrune(t *asynq.Task) (RetentionPrunePayload, error) {
	var p RetentionPrunePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return p, fmt.Errorf("unmarshal retention-prune payload: %w", err)
	}
	if p.CutoffMs <= 0 {
		return p, fmt.Errorf("retention-prune payload: non-positive cutoffMs")
	}
	return p, nil
}
