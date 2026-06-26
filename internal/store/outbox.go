package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// OutboxRepo is the repository for outbox (async send queue, §5/§8). Idempotency
// is enforced by the unique (organization_id, idempotency_key).
type OutboxRepo struct {
	db dbExecQuerier
}

// NewOutboxRepo constructs an OutboxRepo.
func NewOutboxRepo(db dbExecQuerier) *OutboxRepo { return &OutboxRepo{db: db} }

const outboxCols = `id, organization_id, session_id, idempotency_key, payload, status,
	attempts, wa_message_id, error, created_at, updated_at`

func scanOutbox(s rowScanner) (domain.OutboxEntry, error) {
	var (
		o       domain.OutboxEntry
		payload []byte
	)
	err := s.Scan(
		&o.ID, &o.OrganizationID, &o.SessionID, &o.IdempotencyKey, &payload, &o.Status,
		&o.Attempts, &o.WAMessageID, &o.Error, &o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		return domain.OutboxEntry{}, err
	}
	if len(payload) > 0 {
		o.Payload = append([]byte(nil), payload...)
	}
	return o, nil
}

// Insert appends a queued outbox entry. The unique (organization_id, idempotency_key)
// makes a duplicate idempotency key a conflict the caller resolves via
// GetByIdempotency (§8 replay semantics).
func (r *OutboxRepo) Insert(ctx context.Context, o domain.OutboxEntry) error {
	const q = `INSERT INTO outbox
(id, organization_id, session_id, idempotency_key, payload, status, attempts,
 wa_message_id, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := r.db.ExecContext(ctx, q,
		o.ID, o.OrganizationID, o.SessionID, o.IdempotencyKey, []byte(o.Payload), o.Status,
		o.Attempts, o.WAMessageID, o.Error, o.CreatedAt, o.UpdatedAt,
	); err != nil {
		return fmt.Errorf("store: insert outbox: %w", err)
	}
	return nil
}

// Get fetches an outbox entry by id. Maps no-rows to not_found.
func (r *OutboxRepo) Get(ctx context.Context, id string) (domain.OutboxEntry, error) {
	q := "SELECT " + outboxCols + " FROM outbox WHERE id = ?"
	o, err := scanOutbox(r.db.QueryRowContext(ctx, q, id))
	if err != nil {
		return domain.OutboxEntry{}, notFound(err, "outbox entry")
	}
	return o, nil
}

// GetByIdempotency returns the prior entry for (organization_id, idempotency_key) — the
// §8 idempotent replay lookup. Maps no-rows to not_found so the caller knows to
// proceed with a fresh send.
func (r *OutboxRepo) GetByIdempotency(ctx context.Context, organizationID, idempotencyKey string) (domain.OutboxEntry, error) {
	q := "SELECT " + outboxCols + " FROM outbox WHERE organization_id = ? AND idempotency_key = ?"
	o, err := scanOutbox(r.db.QueryRowContext(ctx, q, organizationID, idempotencyKey))
	if err != nil {
		return domain.OutboxEntry{}, notFound(err, "outbox entry")
	}
	return o, nil
}

// UpdateStatus transitions an entry's status and stamps the result fields
// (wa_message_id on success, error on failure), bumping updated_at. waMessageID
// and errMsg may be nil.
func (r *OutboxRepo) UpdateStatus(ctx context.Context, id string, status domain.OutboxStatus, waMessageID, errMsg *string, updatedAt int64) error {
	const q = `UPDATE outbox SET status=?, wa_message_id=?, error=?, updated_at=? WHERE id=?`
	res, err := r.db.ExecContext(ctx, q, status, waMessageID, errMsg, updatedAt, id)
	if err != nil {
		return fmt.Errorf("store: update outbox status: %w", err)
	}
	return affectedOrNotFound(res, "outbox entry")
}

// ClaimQueued atomically moves up to limit queued entries to 'sending',
// returning the claimed rows so the async worker can process them. The
// UPDATE...ORDER BY...LIMIT marks the batch; a follow-up SELECT reads it back.
// v1 is single-instance (§3) so the small race window between the two statements
// is acceptable; multi-instance would use SELECT ... FOR UPDATE SKIP LOCKED in a
// txn. attempts is incremented on claim.
func (r *OutboxRepo) ClaimQueued(ctx context.Context, limit int, updatedAt int64) ([]domain.OutboxEntry, error) {
	limit = normLimit(limit)

	// Mark the batch as sending. We tag exactly this claim via updated_at so the
	// read-back selects only the rows we just transitioned.
	const claim = `UPDATE outbox SET status=?, attempts=attempts+1, updated_at=?
WHERE status=? ORDER BY created_at ASC LIMIT ?`
	if _, err := r.db.ExecContext(ctx, claim, domain.OutboxSending, updatedAt, domain.OutboxQueued, limit); err != nil {
		return nil, fmt.Errorf("store: claim outbox: %w", err)
	}

	const sel = `SELECT ` + outboxCols + ` FROM outbox
WHERE status=? AND updated_at=? ORDER BY created_at ASC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, sel, domain.OutboxSending, updatedAt, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read claimed outbox: %w", err)
	}
	defer rows.Close()
	var out []domain.OutboxEntry
	for rows.Next() {
		o, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
