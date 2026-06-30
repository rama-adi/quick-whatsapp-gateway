package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// WebhookDeliveryRepo is the repository for webhook_deliveries — the per-attempt
// delivery ledger driving retries and dead-lettering (§5/§9).
type WebhookDeliveryRepo struct {
	q *storedb.Queries
}

// NewWebhookDeliveryRepo constructs a WebhookDeliveryRepo.
func NewWebhookDeliveryRepo(db storedb.DBTX) *WebhookDeliveryRepo {
	return &WebhookDeliveryRepo{q: storedb.New(db)}
}

func deliveryFromRow(row storedb.WebhookDelivery) domain.WebhookDelivery {
	return domain.WebhookDelivery{
		ID:           row.ID,
		WebhookID:    row.WebhookID,
		EventID:      row.EventID,
		Status:       domain.WebhookDeliveryStatus(row.Status),
		Attempts:     int(row.Attempts),
		ResponseCode: intPtrFromNull32(row.ResponseCode),
		NextRetryAt:  int64PtrFromNull(row.NextRetryAt),
		LastError:    stringPtrFromNull(row.LastError),
		CreatedAt:    row.CreatedAt,
	}
}

// Enqueue inserts a pending delivery for (webhook, event), due immediately
// (next_retry_at = createdAt). Returns the auto-increment id. Dedup by event_id
// is the dispatcher's job (it decides whether to enqueue); §9 dedups downstream.
func (r *WebhookDeliveryRepo) Enqueue(ctx context.Context, webhookID, eventID string, createdAt int64) (uint64, error) {
	res, err := r.q.EnqueueWebhookDelivery(ctx, storedb.EnqueueWebhookDeliveryParams{
		WebhookID:   webhookID,
		EventID:     eventID,
		Status:      storedb.WebhookDeliveriesStatus(domain.DeliveryPending),
		NextRetryAt: sqlNullInt64(createdAt),
		CreatedAt:   createdAt,
	})
	if err != nil {
		return 0, fmt.Errorf("store: enqueue delivery: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: enqueue delivery id: %w", err)
	}
	return uint64(id), nil
}

// ClaimDue returns up to limit deliveries that are due for a (re)attempt: status
// pending or failed with next_retry_at <= now. Ordered by next_retry_at so the
// oldest-due fire first. Note: this is a plain read; a multi-worker deployment
// would add row-locking, but v1 is single-instance (§3) so a read suffices.
func (r *WebhookDeliveryRepo) ClaimDue(ctx context.Context, now int64, limit int) ([]domain.WebhookDelivery, error) {
	limit = normLimit(limit)
	rows, err := r.q.ClaimDueWebhookDeliveries(ctx, storedb.ClaimDueWebhookDeliveriesParams{
		Status:      storedb.WebhookDeliveriesStatus(domain.DeliveryPending),
		Status_2:    storedb.WebhookDeliveriesStatus(domain.DeliveryFailed),
		NextRetryAt: sqlNullInt64(now),
		Limit:       int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("store: claim due deliveries: %w", err)
	}
	out := make([]domain.WebhookDelivery, 0, len(rows))
	for _, row := range rows {
		out = append(out, deliveryFromRow(row))
	}
	return out, nil
}

// MarkDelivered records a successful delivery: status delivered, attempts++,
// response code stamped, retry cleared.
func (r *WebhookDeliveryRepo) MarkDelivered(ctx context.Context, id uint64, responseCode int) error {
	n, err := r.q.MarkWebhookDeliveryDelivered(ctx, storedb.MarkWebhookDeliveryDeliveredParams{
		Status:       storedb.WebhookDeliveriesStatus(domain.DeliveryDelivered),
		ResponseCode: sqlNullInt32(responseCode),
		ID:           id,
	})
	if err != nil {
		return fmt.Errorf("store: mark delivered: %w", err)
	}
	return rowsAffectedOrNotFound(n, "webhook delivery")
}

// MarkFailed records a failed attempt that will be retried: status failed,
// attempts++, error/response stamped, and the next_retry_at the dispatcher
// computed from the retry policy. responseCode may be nil (e.g. connection
// error with no HTTP response).
func (r *WebhookDeliveryRepo) MarkFailed(ctx context.Context, id uint64, responseCode *int, lastError string, nextRetryAt int64) error {
	n, err := r.q.MarkWebhookDeliveryFailed(ctx, storedb.MarkWebhookDeliveryFailedParams{
		Status:       storedb.WebhookDeliveriesStatus(domain.DeliveryFailed),
		ResponseCode: nullInt32FromPtr(responseCode),
		LastError:    sqlString(lastError),
		NextRetryAt:  sqlNullInt64(nextRetryAt),
		ID:           id,
	})
	if err != nil {
		return fmt.Errorf("store: mark failed: %w", err)
	}
	return rowsAffectedOrNotFound(n, "webhook delivery")
}

// Create inserts a pending delivery from a domain.WebhookDelivery, populating
// the auto-increment id on d. It is the value-shaped sibling of Enqueue used by
// the webhooks.Enqueuer (which builds the row in memory first).
func (r *WebhookDeliveryRepo) Create(ctx context.Context, d *domain.WebhookDelivery) error {
	id, err := r.Enqueue(ctx, d.WebhookID, d.EventID, d.CreatedAt)
	if err != nil {
		return err
	}
	d.ID = id
	d.Status = domain.DeliveryPending
	return nil
}

// ExistsTerminal reports whether a delivery for (webhookID, eventID) is already
// in a terminal state (delivered or dead) — the §9 dedup guard.
func (r *WebhookDeliveryRepo) ExistsTerminal(ctx context.Context, webhookID, eventID string) (bool, error) {
	ok, err := r.q.WebhookDeliveryTerminalExists(ctx, storedb.WebhookDeliveryTerminalExistsParams{
		WebhookID: webhookID,
		EventID:   eventID,
		Status:    storedb.WebhookDeliveriesStatus(domain.DeliveryDelivered),
		Status_2:  storedb.WebhookDeliveriesStatus(domain.DeliveryDead),
	})
	if err != nil {
		return false, fmt.Errorf("store: exists terminal delivery: %w", err)
	}
	return ok, nil
}

// MarkDead records retry exhaustion: status dead, attempts++, error stamped,
// retry cleared so it is never picked up again.
func (r *WebhookDeliveryRepo) MarkDead(ctx context.Context, id uint64, responseCode *int, lastError string) error {
	n, err := r.q.MarkWebhookDeliveryDead(ctx, storedb.MarkWebhookDeliveryDeadParams{
		Status:       storedb.WebhookDeliveriesStatus(domain.DeliveryDead),
		ResponseCode: nullInt32FromPtr(responseCode),
		LastError:    sqlString(lastError),
		ID:           id,
	})
	if err != nil {
		return fmt.Errorf("store: mark dead: %w", err)
	}
	return rowsAffectedOrNotFound(n, "webhook delivery")
}
