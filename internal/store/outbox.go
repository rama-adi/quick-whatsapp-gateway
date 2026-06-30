package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// OutboxRepo is the repository for outbox (async send queue, §5/§8). Idempotency
// is enforced by the unique (organization_id, idempotency_key).
type OutboxRepo struct {
	q *storedb.Queries
}

// NewOutboxRepo constructs an OutboxRepo.
func NewOutboxRepo(db storedb.DBTX) *OutboxRepo { return &OutboxRepo{q: storedb.New(db)} }

func outboxFromRow(row storedb.Outbox) domain.OutboxEntry {
	o := domain.OutboxEntry{
		ID:             row.ID,
		OrganizationID: row.OrganizationID,
		SessionID:      row.SessionID,
		IdempotencyKey: stringPtrFromNull(row.IdempotencyKey),
		Status:         domain.OutboxStatus(row.Status),
		Attempts:       int(row.Attempts),
		WAMessageID:    stringPtrFromNull(row.WaMessageID),
		Error:          stringPtrFromNull(row.Error),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	if len(row.Payload) > 0 {
		o.Payload = append([]byte(nil), row.Payload...)
	}
	return o
}

// Insert appends a queued outbox entry. The unique (organization_id, idempotency_key)
// makes a duplicate idempotency key a conflict the caller resolves via
// GetByIdempotency (§8 replay semantics).
func (r *OutboxRepo) Insert(ctx context.Context, o domain.OutboxEntry) error {
	err := r.q.InsertOutbox(ctx, storedb.InsertOutboxParams{
		ID:             o.ID,
		OrganizationID: o.OrganizationID,
		SessionID:      o.SessionID,
		IdempotencyKey: nullString(o.IdempotencyKey),
		Payload:        o.Payload,
		Status:         storedb.OutboxStatus(o.Status),
		Attempts:       int32(o.Attempts),
		WaMessageID:    nullString(o.WAMessageID),
		Error:          nullString(o.Error),
		CreatedAt:      o.CreatedAt,
		UpdatedAt:      o.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("store: insert outbox: %w", err)
	}
	return nil
}

// Get fetches an outbox entry by id. Maps no-rows to not_found.
func (r *OutboxRepo) Get(ctx context.Context, id string) (domain.OutboxEntry, error) {
	row, err := r.q.GetOutbox(ctx, storedb.GetOutboxParams{ID: id})
	if err != nil {
		return domain.OutboxEntry{}, notFound(err, "outbox entry")
	}
	return outboxFromRow(row), nil
}

// GetByIdempotency returns the prior entry for (organization_id, idempotency_key) — the
// §8 idempotent replay lookup. Maps no-rows to not_found so the caller knows to
// proceed with a fresh send.
func (r *OutboxRepo) GetByIdempotency(ctx context.Context, organizationID, idempotencyKey string) (domain.OutboxEntry, error) {
	row, err := r.q.GetOutboxByIdempotency(ctx, storedb.GetOutboxByIdempotencyParams{
		OrganizationID: organizationID,
		IdempotencyKey: sqlString(idempotencyKey),
	})
	if err != nil {
		return domain.OutboxEntry{}, notFound(err, "outbox entry")
	}
	return outboxFromRow(row), nil
}

// UpdateStatus transitions an entry's status and stamps the result fields
// (wa_message_id on success, error on failure), bumping updated_at. waMessageID
// and errMsg may be nil.
func (r *OutboxRepo) UpdateStatus(ctx context.Context, id string, status domain.OutboxStatus, waMessageID, errMsg *string, updatedAt int64) error {
	// Once a send has succeeded, strip any inline media bytes from the stored
	// payload — the file content is only needed until the row is dispatched and
	// must not be retained afterward. JSON_REMOVE is a no-op for non-media
	// payloads (the '$.media.data' path simply isn't present). The bytes are kept
	// on a failed row so the async worker can still retry it.
	var (
		n   int64
		err error
	)
	if status == domain.OutboxSent {
		n, err = r.q.UpdateOutboxStatusAndStripMedia(ctx, storedb.UpdateOutboxStatusAndStripMediaParams{
			Status:      storedb.OutboxStatus(status),
			WaMessageID: nullString(waMessageID),
			Error:       nullString(errMsg),
			UpdatedAt:   updatedAt,
			ID:          id,
		})
	} else {
		n, err = r.q.UpdateOutboxStatus(ctx, storedb.UpdateOutboxStatusParams{
			Status:      storedb.OutboxStatus(status),
			WaMessageID: nullString(waMessageID),
			Error:       nullString(errMsg),
			UpdatedAt:   updatedAt,
			ID:          id,
		})
	}
	if err != nil {
		return fmt.Errorf("store: update outbox status: %w", err)
	}
	return rowsAffectedOrNotFound(n, "outbox entry")
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
	if err := r.q.ClaimQueuedOutbox(ctx, storedb.ClaimQueuedOutboxParams{
		Status:    storedb.OutboxStatus(domain.OutboxSending),
		UpdatedAt: updatedAt,
		Status_2:  storedb.OutboxStatus(domain.OutboxQueued),
		Limit:     int32(limit),
	}); err != nil {
		return nil, fmt.Errorf("store: claim outbox: %w", err)
	}

	rows, err := r.q.ListClaimedOutbox(ctx, storedb.ListClaimedOutboxParams{
		Status:    storedb.OutboxStatus(domain.OutboxSending),
		UpdatedAt: updatedAt,
		Limit:     int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("store: read claimed outbox: %w", err)
	}
	out := make([]domain.OutboxEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, outboxFromRow(row))
	}
	return out, nil
}
