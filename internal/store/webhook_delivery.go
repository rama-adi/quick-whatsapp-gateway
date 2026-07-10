package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// WebhookDeliveryRepo is the repository for webhook_deliveries — the per-attempt
// delivery ledger driving retries and dead-lettering (§5/§9).
type WebhookDeliveryRepo struct {
	db storedb.DBTX
	q  *storedb.Queries
}

// NewWebhookDeliveryRepo constructs a WebhookDeliveryRepo over a database or an
// existing transaction. ClaimDue requires one of those concrete ownership
// forms because row locks are useful only when their transaction lifetime is
// explicit; other generated-query adapters can use non-claim methods but a
// claim through them fails closed.
func NewWebhookDeliveryRepo(db storedb.DBTX) *WebhookDeliveryRepo {
	return &WebhookDeliveryRepo{db: db, q: storedb.New(db)}
}

// webhookAttemptLeaseMs is the per-item budget in a sequential claimed batch.
// Production HTTP attempts time out after 30 seconds; doubling that leaves room
// for DB bookkeeping. Leases are staggered by batch position so later items do
// not expire while earlier requests are still running.
const webhookAttemptLeaseMs int64 = 60_000

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
// is enforced by the database. Re-enqueueing the same pair returns the existing
// id without resetting its delivery state.
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
// oldest-due fire first. Selection and leasing happen in one transaction with
// row locks and SKIP LOCKED, so concurrent dispatchers receive disjoint batches.
// The lease expires after a worker crash, preserving retry durability.
func (r *WebhookDeliveryRepo) ClaimDue(ctx context.Context, now int64, limit int) ([]domain.WebhookDelivery, error) {
	limit = normLimit(limit)
	if _, ok := r.db.(*sql.Tx); ok {
		// The caller owns the surrounding transaction and therefore the lock
		// lifetime. This supports composing a claim with additional atomic work.
		return r.claimDue(ctx, r.q, now, limit)
	}
	db, ok := r.db.(*sql.DB)
	if !ok {
		return nil, fmt.Errorf("store: claim due deliveries requires *sql.DB or *sql.Tx")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin claim due deliveries: %w", err)
	}
	defer tx.Rollback()

	rows, err := r.claimDue(ctx, storedb.New(tx), now, limit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit claim due deliveries: %w", err)
	}
	return rows, nil
}

// claimDue runs inside a transaction owned either by ClaimDue or its caller.
// Selected rows stay locked while every lease is written; any error aborts the
// batch so another worker can retry the complete set instead of observing a
// partially claimed page.
func (r *WebhookDeliveryRepo) claimDue(ctx context.Context, q *storedb.Queries, now int64, limit int) ([]domain.WebhookDelivery, error) {
	rows, err := q.ClaimDueWebhookDeliveries(ctx, storedb.ClaimDueWebhookDeliveriesParams{
		Status:      storedb.WebhookDeliveriesStatus(domain.DeliveryPending),
		Status_2:    storedb.WebhookDeliveriesStatus(domain.DeliveryFailed),
		NextRetryAt: sqlNullInt64(now),
		Limit:       int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("store: claim due deliveries: %w", err)
	}
	out := make([]domain.WebhookDelivery, 0, len(rows))
	for i, row := range rows {
		leaseUntil := now + webhookAttemptLeaseMs*int64(i+1)
		if _, err := q.LeaseWebhookDelivery(ctx, storedb.LeaseWebhookDeliveryParams{
			NextRetryAt: sqlNullInt64(leaseUntil),
			ID:          row.ID,
		}); err != nil {
			return nil, fmt.Errorf("store: lease webhook delivery %d: %w", row.ID, err)
		}
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
