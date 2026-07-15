package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// retentionDeleteBatchSize bounds each DELETE so the daily retention job does
// not hold a large lock or create one oversized transaction.
const retentionDeleteBatchSize int32 = 1000

// RetentionPruneResult reports rows deleted by a retention pass, grouped by
// table so callers can emit one useful completion record.
type RetentionPruneResult struct {
	WebhookDeliveries int64
	Messages          int64
	EventLog          int64
}

// RetentionRepo owns bounded deletion of data outside the configured retention
// window. It intentionally has no organization scope: retention is a global
// housekeeping operation, scheduled once per gateway database.
type RetentionRepo struct {
	q *storedb.Queries
}

// NewRetentionRepo constructs a RetentionRepo over a database or transaction.
func NewRetentionRepo(db storedb.DBTX) *RetentionRepo { return &RetentionRepo{q: storedb.New(db)} }

// Prune deletes rows older than cutoffMs in short, separately committed
// statements. Terminal webhook delivery history is removed first. Messages are
// then removed by their WhatsApp timestamp. Finally, old event-log entries are
// removed only when no pending or retryable failed delivery still needs the
// event body for an outbound webhook attempt.
func (r *RetentionRepo) Prune(ctx context.Context, cutoffMs int64) (RetentionPruneResult, error) {
	var out RetentionPruneResult

	n, err := r.deleteTerminalDeliveries(ctx, cutoffMs)
	if err != nil {
		return out, err
	}
	out.WebhookDeliveries = n

	n, err = r.deleteMessages(ctx, cutoffMs)
	if err != nil {
		return out, err
	}
	out.Messages = n

	n, err = r.deleteUnreferencedEvents(ctx, cutoffMs)
	if err != nil {
		return out, err
	}
	out.EventLog = n
	return out, nil
}

func (r *RetentionRepo) deleteTerminalDeliveries(ctx context.Context, cutoffMs int64) (int64, error) {
	return deleteBatches(func() (int64, error) {
		n, err := r.q.DeleteTerminalWebhookDeliveriesBefore(ctx, storedb.DeleteTerminalWebhookDeliveriesBeforeParams{
			CreatedAt: cutoffMs,
			Status:    storedb.WebhookDeliveriesStatus(domain.DeliveryDelivered),
			Status_2:  storedb.WebhookDeliveriesStatus(domain.DeliveryDead),
			Limit:     retentionDeleteBatchSize,
		})
		if err != nil {
			return 0, fmt.Errorf("store: delete terminal webhook deliveries: %w", err)
		}
		return n, nil
	})
}

func (r *RetentionRepo) deleteMessages(ctx context.Context, cutoffMs int64) (int64, error) {
	return deleteBatches(func() (int64, error) {
		n, err := r.q.DeleteMessagesBefore(ctx, storedb.DeleteMessagesBeforeParams{
			Timestamp: cutoffMs,
			Limit:     retentionDeleteBatchSize,
		})
		if err != nil {
			return 0, fmt.Errorf("store: delete messages: %w", err)
		}
		return n, nil
	})
}

func (r *RetentionRepo) deleteUnreferencedEvents(ctx context.Context, cutoffMs int64) (int64, error) {
	return deleteBatches(func() (int64, error) {
		n, err := r.q.DeleteUnreferencedEventLogBefore(ctx, storedb.DeleteUnreferencedEventLogBeforeParams{
			CreatedAt: cutoffMs,
			Status:    storedb.WebhookDeliveriesStatus(domain.DeliveryPending),
			Status_2:  storedb.WebhookDeliveriesStatus(domain.DeliveryFailed),
			Limit:     retentionDeleteBatchSize,
		})
		if err != nil {
			return 0, fmt.Errorf("store: delete event log: %w", err)
		}
		return n, nil
	})
}

// deleteBatches repeats a bounded DELETE until it removes fewer rows than one
// full batch. Each generated query call is an independent short statement when
// the repository is backed by *sql.DB.
func deleteBatches(deleteBatch func() (int64, error)) (int64, error) {
	var total int64
	for {
		n, err := deleteBatch()
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(retentionDeleteBatchSize) {
			return total, nil
		}
	}
}
