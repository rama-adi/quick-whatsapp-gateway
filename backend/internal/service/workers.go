package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

// RetentionWorker is the asynq retention-prune consumer (queue.RetentionPruner).
// Outbound sends are not asynq jobs: OutboundScheduler owns the outbox.

// RetentionWorker prunes old rows past the retention cutoff.
type RetentionWorker struct {
	store *store.Store
	log   *slog.Logger
}

// NewRetentionWorker constructs a RetentionWorker.
func NewRetentionWorker(s *store.Store, log *slog.Logger) *RetentionWorker {
	if log == nil {
		log = slog.Default()
	}
	return &RetentionWorker{store: s, log: log}
}

// Prune removes rows older than cutoffMs and logs one aggregate completion
// record. The retention repository batches each DELETE independently so this
// queue task never creates one large transaction.
func (w *RetentionWorker) Prune(ctx context.Context, cutoffMs int64) error {
	if w.store == nil || w.store.Retention == nil {
		return fmt.Errorf("retention prune: retention repository is not configured")
	}
	result, err := w.store.Retention.Prune(ctx, cutoffMs)
	if err != nil {
		return fmt.Errorf("retention prune: %w", err)
	}
	w.log.InfoContext(ctx, "retention prune complete",
		"cutoffMs", cutoffMs,
		"webhookDeliveries", result.WebhookDeliveries,
		"messages", result.Messages,
		"eventLog", result.EventLog)
	return nil
}
