package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

// GatewayEvent is the API persistence boundary for one at-least-once event.
// The control adapter may acknowledge it only after Ingest returns nil.
type GatewayEvent struct {
	EventID, GatewayID, SessionID, OrganizationID, Type string
	ConnectionEpoch, AssignmentEpoch                    uint64
	Payload                                             []byte
	OccurredAt                                          int64
}

// CommittedEventWork describes one multi-replica-safe claim attempt against
// ingested events whose post-commit consumers have not all accepted them yet.
type CommittedEventWork struct {
	Owner      string
	ClaimedAt  time.Time
	LeaseUntil time.Time
	MaxItems   int
}

type GatewayEventIngestRepo struct {
	db           storedb.DBTX
	MediaCapture func(context.Context, storedb.DBTX, GatewayEvent) ([]byte, error)
}

func NewGatewayEventIngestRepo(db storedb.DBTX) *GatewayEventIngestRepo {
	return &GatewayEventIngestRepo{db: db}
}

// begin owns a fresh transaction unless the caller already supplied one,
// mirroring the webhook delivery claim pattern. An externally owned transaction
// never commits or rolls back through this helper.
func (r *GatewayEventIngestRepo) begin(ctx context.Context) (exec storedb.DBTX, commit func() error, rollback func() error, err error) {
	if tx, ok := r.db.(*sql.Tx); ok {
		return tx, func() error { return nil }, func() error { return nil }, nil
	}
	db, ok := r.db.(*sql.DB)
	if !ok {
		return nil, nil, nil, fmt.Errorf("store: gateway event ingest requires *sql.DB or *sql.Tx")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	return tx, func() error { return tx.Commit() }, func() error { return tx.Rollback() }, nil
}

// Ingest validates the current connection and assignment fences in the same
// transaction as both dedupe and event-log insertion. Duplicate gateway event
// IDs are successful no-ops, supporting acknowledgement-loss replay.
func (r *GatewayEventIngestRepo) Ingest(ctx context.Context, event GatewayEvent, committedAt int64) error {
	return r.IngestBatch(ctx, []GatewayEvent{event}, committedAt)
}

// IngestBatch commits all newly observed events together. A stale event rolls
// back the entire batch so the control stream cannot acknowledge a partial set.
func (r *GatewayEventIngestRepo) IngestBatch(ctx context.Context, events []GatewayEvent, committedAt int64) error {
	if len(events) == 0 {
		return fmt.Errorf("empty gateway event batch")
	}
	tx, commit, rollback, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = rollback() }()
	for _, event := range events {
		var n int
		err = tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM gateway_session_assignments a JOIN wa_sessions s ON s.id=a.session_id JOIN gateways g ON g.id=a.gateway_id WHERE a.gateway_id=? AND a.session_id=? AND s.organization_id=? AND a.assignment_epoch=? AND g.connection_epoch=? AND g.deleted_at IS NULL`,
			event.GatewayID, event.SessionID, event.OrganizationID, event.AssignmentEpoch, event.ConnectionEpoch,
		).Scan(&n)
		if err != nil {
			return fmt.Errorf("store: check gateway event ownership: %w", err)
		}
		if n != 1 {
			return ErrGatewayEventStale
		}
		result, err := tx.ExecContext(ctx,
			`INSERT IGNORE INTO gateway_ingested_events (gateway_event_id,gateway_id,connection_epoch,session_id,assignment_epoch,organization_id,event_log_id,committed_at) VALUES (?,?,?,?,?,?,?,?)`,
			event.EventID,
			event.GatewayID,
			event.ConnectionEpoch,
			event.SessionID,
			event.AssignmentEpoch,
			event.OrganizationID,
			event.EventID,
			committedAt,
		)
		if err != nil {
			return err
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			continue
		}
		// API sends and gateway own-message echoes race for one public event
		// identity. The first committed claim owns event_log and fan-out; every
		// gateway journal ID still receives a durable acknowledgement.
		if event.Type == domain.EventMessageFromMe {
			var identity struct {
				WAMessageID string `json:"waMessageId"`
				ChatJID     string `json:"chatJid"`
				FromMe      bool   `json:"fromMe"`
			}
			if json.Unmarshal(event.Payload, &identity) == nil && identity.FromMe &&
				identity.WAMessageID != "" && identity.ChatJID != "" {
				var claimedID, claimedOwner string
				claimedID, claimedOwner, err = claimOutgoingEvent(ctx, tx, outgoingEventIdentity{
					organizationID: event.OrganizationID, sessionID: event.SessionID,
					chatJID: identity.ChatJID, waMessageID: identity.WAMessageID,
				}, event.EventID, "gateway")
				if err != nil {
					return err
				}
				if claimedOwner != "gateway" || claimedID != event.EventID {
					if _, err = tx.ExecContext(ctx,
						`UPDATE gateway_ingested_events SET completed_at=? WHERE gateway_event_id=?`,
						committedAt, event.EventID,
					); err != nil {
						return fmt.Errorf("store: complete duplicate sent echo: %w", err)
					}
					continue
				}
			}
		}
		if r.MediaCapture != nil {
			event.Payload, err = r.MediaCapture(ctx, tx, event)
			if err != nil {
				return err
			}
		} else {
			// Strip private descriptors even when attachment storage is disabled.
			var payload map[string]json.RawMessage
			if json.Unmarshal(event.Payload, &payload) == nil && payload["_mediaSource"] != nil {
				delete(payload, "_mediaSource")
				event.Payload, err = json.Marshal(payload)
				if err != nil {
					return err
				}
			}
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO event_log (event_id,organization_id,session_id,type,payload,created_at) VALUES (?,?,?,?,?,?)`,
			event.EventID, event.OrganizationID, event.SessionID, event.Type, event.Payload, event.OccurredAt,
		); err != nil {
			return err
		}
	}
	if err = commit(); err != nil {
		return err
	}
	return nil
}

var ErrGatewayEventStale = errors.New("gateway event stale")

var errCommittedEventNotClaimed = errors.New("committed event is not claimed by this owner")

// ClaimCommittedEvents leases up to work.MaxItems ingested-but-incomplete
// events for post-commit fan-out. Selection and leasing happen in one
// transaction with row locks and SKIP LOCKED, so concurrent workers receive
// disjoint batches; an expired lease makes a crashed worker's claims available
// again without re-ingesting the durable envelope. Events are returned oldest
// committed first.
func (r *GatewayEventIngestRepo) ClaimCommittedEvents(ctx context.Context, work CommittedEventWork) ([]domain.Event, error) {
	if work.Owner == "" {
		return nil, fmt.Errorf("store: committed event owner is required")
	}
	if work.MaxItems <= 0 {
		return nil, fmt.Errorf("store: committed event max items must be positive")
	}
	if work.ClaimedAt.IsZero() || !work.LeaseUntil.After(work.ClaimedAt) {
		return nil, fmt.Errorf("store: committed event lease must end after claim time")
	}
	tx, commit, rollback, err := r.begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: begin claim committed events: %w", err)
	}
	defer func() { _ = rollback() }()

	// Eligibility uses the current claim time, not the new lease deadline.
	// Comparing against the future deadline would steal another worker's live lease.
	leaseUntilMs := work.LeaseUntil.UnixMilli()
	rows, err := tx.QueryContext(ctx,
		`SELECT i.event_log_id, e.type, e.organization_id, e.session_id, e.created_at, e.payload FROM gateway_ingested_events i JOIN event_log e ON e.event_id=i.event_log_id WHERE i.completed_at IS NULL AND (i.lease_until IS NULL OR i.lease_until<=?) AND NOT EXISTS (SELECT 1 FROM gateway_ingested_events prior WHERE prior.session_id=i.session_id AND prior.completed_at IS NULL AND prior.lease_until>? AND (prior.committed_at, prior.event_log_id)<(i.committed_at, i.event_log_id)) ORDER BY i.committed_at, i.event_log_id LIMIT ? FOR UPDATE SKIP LOCKED`,
		work.ClaimedAt.UnixMilli(), work.ClaimedAt.UnixMilli(), work.MaxItems,
	)
	if err != nil {
		return nil, fmt.Errorf("store: select claimable committed events: %w", err)
	}
	type claimedRow struct {
		eventID      string
		typ          string
		organization string
		session      string
		createdAt    int64
		payload      []byte
	}
	var claimed []claimedRow
	for rows.Next() {
		var row claimedRow
		if err := rows.Scan(&row.eventID, &row.typ, &row.organization, &row.session, &row.createdAt, &row.payload); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("store: scan claimable committed events: %w", err)
		}
		claimed = append(claimed, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("store: iterate claimable committed events: %w", err)
	}
	_ = rows.Close()

	events := make([]domain.Event, 0, len(claimed))
	for _, row := range claimed {
		result, err := tx.ExecContext(ctx,
			`UPDATE gateway_ingested_events SET claimed_by=?, lease_until=? WHERE event_log_id=? AND completed_at IS NULL`,
			work.Owner, leaseUntilMs, row.eventID,
		)
		if err != nil {
			return nil, fmt.Errorf("store: lease committed event %s: %w", row.eventID, err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, fmt.Errorf("store: lease committed event %s: lost claim", row.eventID)
		}
		events = append(events, domain.Event{
			Schema:       domain.Schema,
			ID:           row.eventID,
			Type:         row.typ,
			Session:      row.session,
			Organization: row.organization,
			Timestamp:    row.createdAt,
			Payload:      json.RawMessage(row.payload),
		})
	}
	if err := commit(); err != nil {
		return nil, fmt.Errorf("store: commit claim committed events: %w", err)
	}
	return events, nil
}

// CompleteCommittedEvent permanently records that every post-commit consumer
// accepted one event. The write is fenced by the claiming owner; completing an
// already-completed event by its owner stays a success so a lost completion
// response cannot fail a retry.
func (r *GatewayEventIngestRepo) CompleteCommittedEvent(ctx context.Context, owner, eventID string, completedAt time.Time) error {
	if owner == "" || eventID == "" {
		return fmt.Errorf("store: committed event owner and id are required")
	}
	result, err := r.db.ExecContext(ctx,
		`UPDATE gateway_ingested_events SET completed_at=? WHERE event_log_id=? AND claimed_by=? AND completed_at IS NULL`,
		completedAt.UnixMilli(), eventID, owner,
	)
	if err != nil {
		return fmt.Errorf("store: complete committed event %s: %w", eventID, err)
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		return nil
	}
	var completedByOwner bool
	err = r.db.QueryRowContext(ctx,
		`SELECT (claimed_by = ?) AND (completed_at IS NOT NULL) FROM gateway_ingested_events WHERE event_log_id=?`,
		owner, eventID,
	).Scan(&completedByOwner)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errCommittedEventNotClaimed
		}
		return fmt.Errorf("store: complete committed event %s: %w", eventID, err)
	}
	if !completedByOwner {
		return errCommittedEventNotClaimed
	}
	return nil
}
