package queue

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hibiken/asynq"
)

// Task type names. asynq dispatches on these strings, so they double as the
// stable wire identifiers for a job kind. Namespaced to avoid collisions if this
// Redis instance is ever shared.
const TypeRetentionPrune = "retention:prune"

// QueueRetention is the asynq queue retention tasks are routed to. Outbound
// sends and webhook deliveries are not asynq tasks: the API's durable MySQL
// work tables (outbox, webhook_deliveries) own them.
const QueueRetention = "retention"

// RetentionPrunePayload is the JSON body of a TypeRetentionPrune task. CutoffMs
// is an epoch-ms (domain.NowMs) lower bound: rows older than this are deleted.
// Computed by the enqueuer from RETENTION_DAYS so the worker stays policy-free.
type RetentionPrunePayload struct {
	CutoffMs int64 `json:"cutoffMs"`
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

// parseRetentionPrune decodes a task payload into RetentionPrunePayload.
func parseRetentionPrune(t *asynq.Task) (RetentionPrunePayload, error) {
	var p RetentionPrunePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return p, fmt.Errorf("unmarshal retention-prune payload: %w", err)
	}
	if p.CutoffMs <= 0 {
		return p, errors.New("retention-prune payload: non-positive cutoffMs")
	}
	return p, nil
}
