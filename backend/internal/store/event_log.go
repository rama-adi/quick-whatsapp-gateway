package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

// EventLogRepo is the repository for event_log (§5/§9) — the durable, monotonic
// event stream backing NDJSON ?since= resume. The surrogate id is the resume
// cursor; event_id (ULID) is the value exposed to clients.
type EventLogRepo struct {
	q  *storedb.Queries
	db storedb.DBTX
}

// NewEventLogRepo constructs an EventLogRepo.
func NewEventLogRepo(db storedb.DBTX) *EventLogRepo { return &EventLogRepo{q: storedb.New(db), db: db} }

// AppendOwnSentOnce makes the API's sent event compete atomically with a
// gateway echo for the same WhatsApp message. Only the claim owner should fan
// out the public event; retries by the API owner may re-run downstream delivery.
func (r *EventLogRepo) AppendOwnSentOnce(
	ctx context.Context,
	e domain.EventLogEntry,
	chatJID, waMessageID string,
) (bool, error) {
	if e.Type != domain.EventMessageFromMe || chatJID == "" || waMessageID == "" {
		return false, fmt.Errorf("store: outgoing sent event identity is required")
	}
	var tx *sql.Tx
	switch db := r.db.(type) {
	case *sql.DB:
		var err error
		tx, err = db.BeginTx(ctx, nil)
		if err != nil {
			return false, fmt.Errorf("store: begin outgoing sent event: %w", err)
		}
	case *sql.Tx:
		tx = db
	default:
		return false, fmt.Errorf("store: outgoing sent event requires SQL transaction")
	}
	ownedTx := tx != r.db
	if ownedTx {
		defer func() { _ = tx.Rollback() }()
	}
	claimedID, claimedOwner, err := claimOutgoingEvent(ctx, tx, outgoingEventIdentity{
		organizationID: e.OrganizationID, sessionID: e.SessionID,
		chatJID: chatJID, waMessageID: waMessageID,
	}, e.EventID, "api")
	if err != nil {
		return false, err
	}
	if claimedOwner != "api" {
		if ownedTx {
			if err := tx.Commit(); err != nil {
				return false, fmt.Errorf("store: commit gateway-owned sent event: %w", err)
			}
		}
		return false, nil
	}
	if claimedID != e.EventID {
		return false, fmt.Errorf("store: conflicting API sent event claim")
	}
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO event_log
		(event_id,organization_id,session_id,type,payload,created_at) VALUES (?,?,?,?,?,?)`,
		e.EventID, e.OrganizationID, e.SessionID, e.Type, e.Payload, e.CreatedAt); err != nil {
		return false, fmt.Errorf("store: append API sent event: %w", err)
	}
	var organizationID, sessionID, typ string
	if err := tx.QueryRowContext(ctx, `SELECT organization_id,session_id,type FROM event_log WHERE event_id=?`,
		e.EventID).Scan(&organizationID, &sessionID, &typ); err != nil {
		return false, fmt.Errorf("store: verify API sent event: %w", err)
	}
	if organizationID != e.OrganizationID || sessionID != e.SessionID || typ != e.Type {
		return false, fmt.Errorf("store: API sent event identity collision")
	}
	if ownedTx {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("store: commit API sent event: %w", err)
		}
	}
	return true, nil
}

func eventLogFromRow(row storedb.EventLog) domain.EventLogEntry {
	e := domain.EventLogEntry{
		ID:             row.ID,
		EventID:        row.EventID,
		OrganizationID: row.OrganizationID,
		SessionID:      row.SessionID,
		Type:           row.Type,
		CreatedAt:      row.CreatedAt,
	}
	if len(row.Payload) > 0 {
		e.Payload = append([]byte(nil), row.Payload...)
	}
	return e
}

// Append writes an event to the log. event_id is unique (dedup); the surrogate
// id is assigned by MySQL and returned for use as a resume cursor.
func (r *EventLogRepo) Append(ctx context.Context, e domain.EventLogEntry) (uint64, error) {
	res, err := r.q.AppendEventLog(ctx, storedb.AppendEventLogParams{
		EventID:        e.EventID,
		OrganizationID: e.OrganizationID,
		SessionID:      e.SessionID,
		Type:           e.Type,
		Payload:        e.Payload,
		CreatedAt:      e.CreatedAt,
	})
	if err != nil {
		return 0, fmt.Errorf("store: append event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: append event id: %w", err)
	}
	return uint64(id), nil
}

// ListSince returns up to limit events for a organization after the given cursor id,
// optionally filtered to one session (sessionID == "" = all the organization's
// sessions). Ordered by id ASC — the monotonic cursor — so the stream replays in
// order and the next cursor is the last returned id. This backs §9 ?since=.
func (r *EventLogRepo) ListSince(
	ctx context.Context,
	organizationID, sessionID string,
	afterID uint64,
	limit int,
) ([]domain.EventLogEntry, error) {
	limit = normLimit(limit)

	var rows []storedb.EventLog
	var err error
	if sessionID == "" {
		rows, err = r.q.ListEventLogSinceForOrg(ctx, storedb.ListEventLogSinceForOrgParams{
			OrganizationID: organizationID,
			ID:             afterID,
			Limit:          int32(limit),
		})
	} else {
		rows, err = r.q.ListEventLogSinceForSession(ctx, storedb.ListEventLogSinceForSessionParams{
			OrganizationID: organizationID,
			SessionID:      sessionID,
			ID:             afterID,
			Limit:          int32(limit),
		})
	}
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	out := make([]domain.EventLogEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, eventLogFromRow(row))
	}
	return out, nil
}

// GetByEventID fetches a logged event by its public ULID event_id. Maps no-rows
// to not_found.
func (r *EventLogRepo) GetByEventID(ctx context.Context, eventID string) (domain.EventLogEntry, error) {
	row, err := r.q.GetEventLogByEventID(ctx, storedb.GetEventLogByEventIDParams{EventID: eventID})
	if err != nil {
		return domain.EventLogEntry{}, notFound(err, "event")
	}
	return eventLogFromRow(row), nil
}

// GetEvent loads a logged event and projects it onto the wire domain.Event
// envelope (§9) — used by the webhook dispatcher to reload the body to POST.
func (r *EventLogRepo) GetEvent(ctx context.Context, eventID string) (domain.Event, error) {
	e, err := r.GetByEventID(ctx, eventID)
	if err != nil {
		return domain.Event{}, err
	}
	return domain.Event{
		Schema:       domain.Schema,
		ID:           e.EventID,
		Type:         e.Type,
		Session:      e.SessionID,
		Organization: e.OrganizationID,
		Timestamp:    e.CreatedAt,
		Payload:      e.Payload,
	}, nil
}
