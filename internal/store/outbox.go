package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// OutboxRepo is the repository for outbox (async send queue, §5/§8). Idempotency
// is enforced by the unique (organization_id, idempotency_key).
type OutboxRepo struct {
	db storedb.DBTX
	q  *storedb.Queries
}

// NewOutboxRepo constructs an OutboxRepo. Batch claims require a concrete
// *sql.DB or caller-owned *sql.Tx so row-lock ownership cannot be ambiguous.
func NewOutboxRepo(db storedb.DBTX) *OutboxRepo { return &OutboxRepo{db: db, q: storedb.New(db)} }

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
	// payloads (the media paths simply aren't present). This covers the single
	// '$.media.data' field and all ten possible '$.medias[n].data' album fields.
	// The bytes are kept
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

// ClaimByID atomically transfers one dispatchable row to a worker by changing
// queued (initial attempt), failed (Asynq retry), or a sending row whose lease
// timestamp is at/before staleBefore to sending. It increments attempts and
// stamps updatedAt in the same CAS. claimed is true only for the process that
// changed the row; false includes an active sending lease, terminal sent row,
// missing row, or concurrent winner. The caller chooses staleBefore from a
// lease duration longer than its maximum dispatch operation.
func (r *OutboxRepo) ClaimByID(ctx context.Context, id string, updatedAt, staleBefore int64) (claimed bool, err error) {
	n, err := r.q.ClaimOutboxByID(ctx, storedb.ClaimOutboxByIDParams{
		ClaimedStatus: storedb.OutboxStatus(domain.OutboxSending),
		UpdatedAt:     updatedAt,
		ID:            id,
		QueuedStatus:  storedb.OutboxStatus(domain.OutboxQueued),
		FailedStatus:  storedb.OutboxStatus(domain.OutboxFailed),
		SendingStatus: storedb.OutboxStatus(domain.OutboxSending),
		StaleBefore:   staleBefore,
	})
	if err != nil {
		return false, fmt.Errorf("store: claim outbox %s: %w", id, err)
	}
	switch n {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("store: claim outbox %s affected %d rows", id, n)
	}
}

// ClaimQueued atomically moves up to limit queued entries to 'sending',
// returning the claimed rows so an async worker can process them. Selection uses
// FOR UPDATE SKIP LOCKED and every row is transitioned with the same CAS used by
// ClaimByID before commit, so worker replicas receive disjoint batches. Any
// statement or commit failure rolls back the complete batch.
func (r *OutboxRepo) ClaimQueued(ctx context.Context, limit int, updatedAt int64) ([]domain.OutboxEntry, error) {
	return r.claimQueuedForSession(ctx, "", limit, updatedAt)
}

// ClaimQueuedForSession is ClaimQueued constrained to one session. The filter
// is applied inside the locking SELECT—not after rows become sending—so a
// session-specific consumer cannot claim and then discard another session's
// work. An empty sessionID intentionally selects across all sessions.
func (r *OutboxRepo) ClaimQueuedForSession(ctx context.Context, sessionID string, limit int, updatedAt int64) ([]domain.OutboxEntry, error) {
	return r.claimQueuedForSession(ctx, sessionID, limit, updatedAt)
}

func (r *OutboxRepo) claimQueuedForSession(ctx context.Context, sessionID string, limit int, updatedAt int64) ([]domain.OutboxEntry, error) {
	limit = normLimit(limit)
	if _, ok := r.db.(*sql.Tx); ok {
		return r.claimQueued(ctx, r.q, sessionID, limit, updatedAt)
	}
	db, ok := r.db.(*sql.DB)
	if !ok {
		return nil, fmt.Errorf("store: claim queued outbox requires *sql.DB or *sql.Tx")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin claim queued outbox: %w", err)
	}
	defer tx.Rollback()
	rows, err := r.claimQueued(ctx, storedb.New(tx), sessionID, limit, updatedAt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit claim queued outbox: %w", err)
	}
	return rows, nil
}

// claimQueued assumes q is transaction-bound. Rows remain locked from selection
// through every CAS; a zero-row CAS is skipped defensively rather than returned
// as owned work.
func (r *OutboxRepo) claimQueued(ctx context.Context, q *storedb.Queries, sessionID string, limit int, updatedAt int64) ([]domain.OutboxEntry, error) {
	rows, err := q.SelectQueuedOutboxForClaim(ctx, storedb.SelectQueuedOutboxForClaimParams{
		QueuedStatus:  storedb.OutboxStatus(domain.OutboxQueued),
		SessionFilter: sessionID,
		Limit:         int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("store: select queued outbox: %w", err)
	}
	out := make([]domain.OutboxEntry, 0, len(rows))
	for _, row := range rows {
		n, err := q.ClaimOutboxByID(ctx, storedb.ClaimOutboxByIDParams{
			ClaimedStatus: storedb.OutboxStatus(domain.OutboxSending),
			UpdatedAt:     updatedAt,
			ID:            row.ID,
			QueuedStatus:  storedb.OutboxStatus(domain.OutboxQueued),
			FailedStatus:  storedb.OutboxStatus(domain.OutboxFailed),
			SendingStatus: storedb.OutboxStatus(domain.OutboxSending),
			StaleBefore:   updatedAt,
		})
		if err != nil {
			return nil, fmt.Errorf("store: claim outbox %s: %w", row.ID, err)
		}
		if n != 1 {
			continue
		}
		entry := outboxFromRow(row)
		entry.Status = domain.OutboxSending
		entry.Attempts++
		entry.UpdatedAt = updatedAt
		out = append(out, entry)
	}
	return out, nil
}
