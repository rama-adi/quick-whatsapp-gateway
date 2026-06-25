package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// WebhookDeliveryRepo is the repository for webhook_deliveries — the per-attempt
// delivery ledger driving retries and dead-lettering (§5/§9).
type WebhookDeliveryRepo struct {
	db dbExecQuerier
}

// NewWebhookDeliveryRepo constructs a WebhookDeliveryRepo.
func NewWebhookDeliveryRepo(db dbExecQuerier) *WebhookDeliveryRepo {
	return &WebhookDeliveryRepo{db: db}
}

const deliveryCols = `id, webhook_id, event_id, status, attempts, response_code,
	next_retry_at, last_error, created_at`

func scanDelivery(s rowScanner) (domain.WebhookDelivery, error) {
	var d domain.WebhookDelivery
	err := s.Scan(
		&d.ID, &d.WebhookID, &d.EventID, &d.Status, &d.Attempts, &d.ResponseCode,
		&d.NextRetryAt, &d.LastError, &d.CreatedAt,
	)
	if err != nil {
		return domain.WebhookDelivery{}, err
	}
	return d, nil
}

// Enqueue inserts a pending delivery for (webhook, event), due immediately
// (next_retry_at = createdAt). Returns the auto-increment id. Dedup by event_id
// is the dispatcher's job (it decides whether to enqueue); §9 dedups downstream.
func (r *WebhookDeliveryRepo) Enqueue(ctx context.Context, webhookID, eventID string, createdAt int64) (uint64, error) {
	const q = `INSERT INTO webhook_deliveries
(webhook_id, event_id, status, attempts, next_retry_at, created_at)
VALUES (?, ?, ?, 0, ?, ?)`
	res, err := r.db.ExecContext(ctx, q, webhookID, eventID, domain.DeliveryPending, createdAt, createdAt)
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
	const q = `SELECT ` + deliveryCols + ` FROM webhook_deliveries
WHERE status IN (?, ?) AND next_retry_at IS NOT NULL AND next_retry_at <= ?
ORDER BY next_retry_at ASC
LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, domain.DeliveryPending, domain.DeliveryFailed, now, limit)
	if err != nil {
		return nil, fmt.Errorf("store: claim due deliveries: %w", err)
	}
	defer rows.Close()
	var out []domain.WebhookDelivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDelivered records a successful delivery: status delivered, attempts++,
// response code stamped, retry cleared.
func (r *WebhookDeliveryRepo) MarkDelivered(ctx context.Context, id uint64, responseCode int) error {
	const q = `UPDATE webhook_deliveries
SET status=?, attempts=attempts+1, response_code=?, next_retry_at=NULL, last_error=NULL
WHERE id=?`
	res, err := r.db.ExecContext(ctx, q, domain.DeliveryDelivered, responseCode, id)
	if err != nil {
		return fmt.Errorf("store: mark delivered: %w", err)
	}
	return affectedOrNotFound(res, "webhook delivery")
}

// MarkFailed records a failed attempt that will be retried: status failed,
// attempts++, error/response stamped, and the next_retry_at the dispatcher
// computed from the retry policy. responseCode may be nil (e.g. connection
// error with no HTTP response).
func (r *WebhookDeliveryRepo) MarkFailed(ctx context.Context, id uint64, responseCode *int, lastError string, nextRetryAt int64) error {
	const q = `UPDATE webhook_deliveries
SET status=?, attempts=attempts+1, response_code=?, last_error=?, next_retry_at=?
WHERE id=?`
	res, err := r.db.ExecContext(ctx, q, domain.DeliveryFailed, responseCode, lastError, nextRetryAt, id)
	if err != nil {
		return fmt.Errorf("store: mark failed: %w", err)
	}
	return affectedOrNotFound(res, "webhook delivery")
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
	const q = `SELECT 1 FROM webhook_deliveries
WHERE webhook_id=? AND event_id=? AND status IN (?, ?) LIMIT 1`
	var one int
	err := r.db.QueryRowContext(ctx, q, webhookID, eventID, domain.DeliveryDelivered, domain.DeliveryDead).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("store: exists terminal delivery: %w", err)
	}
	return true, nil
}

// MarkDead records retry exhaustion: status dead, attempts++, error stamped,
// retry cleared so it is never picked up again.
func (r *WebhookDeliveryRepo) MarkDead(ctx context.Context, id uint64, responseCode *int, lastError string) error {
	const q = `UPDATE webhook_deliveries
SET status=?, attempts=attempts+1, response_code=?, last_error=?, next_retry_at=NULL
WHERE id=?`
	res, err := r.db.ExecContext(ctx, q, domain.DeliveryDead, responseCode, lastError, id)
	if err != nil {
		return fmt.Errorf("store: mark dead: %w", err)
	}
	return affectedOrNotFound(res, "webhook delivery")
}
