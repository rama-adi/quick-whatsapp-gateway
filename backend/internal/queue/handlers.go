package queue

import (
	"context"
	"errors"
	"fmt"

	"github.com/hibiken/asynq"
)

// Consumer interfaces (defined here, implemented by sibling packages). The
// handlers in this package are thin: decode the payload, then delegate to one of
// these. This keeps queue free of sibling-package imports and makes handler
// dispatch trivial to test with fakes.

// RetentionPruner deletes event_log/messages/webhook_deliveries rows older than
// the given epoch-ms cutoff (§5 daily prune).
type RetentionPruner interface {
	Prune(ctx context.Context, cutoffMs int64) error
}

// Handlers bundles the consumer dependencies the worker handlers delegate to.
// Any field may be nil if that job type is not registered for a given server.
type Handlers struct {
	Retention RetentionPruner
}

// handleRetentionPrune decodes the task and delegates to the RetentionPruner.
func (h Handlers) handleRetentionPrune(ctx context.Context, t *asynq.Task) error {
	if h.Retention == nil {
		return errors.New("queue: no RetentionPruner registered")
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
// consumer is non-nil. Exposed so callers and tests can dispatch tasks without
// a running server: mux.Handler(task) resolves the registered HandlerFunc.
func (h Handlers) Mux() *asynq.ServeMux {
	mux := asynq.NewServeMux()
	if h.Retention != nil {
		mux.HandleFunc(TypeRetentionPrune, h.handleRetentionPrune)
	}
	return mux
}
