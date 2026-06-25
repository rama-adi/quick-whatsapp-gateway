package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// EventLogRepo is the repository for event_log (§5/§9) — the durable, monotonic
// event stream backing NDJSON ?since= resume. The surrogate id is the resume
// cursor; event_id (ULID) is the value exposed to clients.
type EventLogRepo struct {
	db dbExecQuerier
}

// NewEventLogRepo constructs an EventLogRepo.
func NewEventLogRepo(db dbExecQuerier) *EventLogRepo { return &EventLogRepo{db: db} }

const eventLogCols = `id, event_id, tenant_id, session_id, type, payload, created_at`

func scanEventLog(s rowScanner) (domain.EventLogEntry, error) {
	var (
		e       domain.EventLogEntry
		payload []byte
	)
	err := s.Scan(&e.ID, &e.EventID, &e.TenantID, &e.SessionID, &e.Type, &payload, &e.CreatedAt)
	if err != nil {
		return domain.EventLogEntry{}, err
	}
	if len(payload) > 0 {
		e.Payload = append([]byte(nil), payload...)
	}
	return e, nil
}

// Append writes an event to the log. event_id is unique (dedup); the surrogate
// id is assigned by MySQL and returned for use as a resume cursor.
func (r *EventLogRepo) Append(ctx context.Context, e domain.EventLogEntry) (uint64, error) {
	const q = `INSERT INTO event_log (event_id, tenant_id, session_id, type, payload, created_at)
VALUES (?, ?, ?, ?, ?, ?)`
	res, err := r.db.ExecContext(ctx, q, e.EventID, e.TenantID, e.SessionID, e.Type, []byte(e.Payload), e.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("store: append event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: append event id: %w", err)
	}
	return uint64(id), nil
}

// ListSince returns up to limit events for a tenant after the given cursor id,
// optionally filtered to one session (sessionID == "" = all the tenant's
// sessions). Ordered by id ASC — the monotonic cursor — so the stream replays in
// order and the next cursor is the last returned id. This backs §9 ?since=.
func (r *EventLogRepo) ListSince(ctx context.Context, tenantID, sessionID string, afterID uint64, limit int) ([]domain.EventLogEntry, error) {
	limit = normLimit(limit)

	var (
		q    string
		args []any
	)
	if sessionID == "" {
		q = "SELECT " + eventLogCols + " FROM event_log WHERE tenant_id = ? AND id > ? ORDER BY id ASC LIMIT ?"
		args = []any{tenantID, afterID, limit}
	} else {
		q = "SELECT " + eventLogCols + " FROM event_log WHERE tenant_id = ? AND session_id = ? AND id > ? ORDER BY id ASC LIMIT ?"
		args = []any{tenantID, sessionID, afterID, limit}
	}

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()
	var out []domain.EventLogEntry
	for rows.Next() {
		e, err := scanEventLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetByEventID fetches a logged event by its public ULID event_id. Maps no-rows
// to not_found.
func (r *EventLogRepo) GetByEventID(ctx context.Context, eventID string) (domain.EventLogEntry, error) {
	q := "SELECT " + eventLogCols + " FROM event_log WHERE event_id = ?"
	e, err := scanEventLog(r.db.QueryRowContext(ctx, q, eventID))
	if err != nil {
		return domain.EventLogEntry{}, notFound(err, "event")
	}
	return e, nil
}
