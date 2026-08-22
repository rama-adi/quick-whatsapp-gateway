package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GatewayEvent is the API persistence boundary for one at-least-once event.
// The control adapter may acknowledge it only after Ingest returns nil.
type GatewayEvent struct {
	EventID, GatewayID, SessionID, OrganizationID, Type string
	ConnectionEpoch, AssignmentEpoch                    uint64
	Payload                                             []byte
	OccurredAt                                          int64
}
type GatewayEventIngestRepo struct{ db *sql.DB }

func NewGatewayEventIngestRepo(db *sql.DB) *GatewayEventIngestRepo {
	return &GatewayEventIngestRepo{db: db}
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
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, event := range events {
		var n int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_session_assignments a JOIN wa_sessions s ON s.id=a.session_id JOIN gateways g ON g.id=a.gateway_id WHERE a.gateway_id=? AND a.session_id=? AND s.organization_id=? AND a.assignment_epoch=? AND g.connection_epoch=? AND g.deleted_at IS NULL`, event.GatewayID, event.SessionID, event.OrganizationID, event.AssignmentEpoch, event.ConnectionEpoch).Scan(&n)
		if err != nil || n != 1 {
			return ErrGatewayEventStale
		}
		result, err := tx.ExecContext(ctx, `INSERT IGNORE INTO gateway_ingested_events (gateway_event_id,gateway_id,connection_epoch,session_id,assignment_epoch,organization_id,event_log_id,committed_at) VALUES (?,?,?,?,?,?,?,?)`, event.EventID, event.GatewayID, event.ConnectionEpoch, event.SessionID, event.AssignmentEpoch, event.OrganizationID, event.EventID, committedAt)
		if err != nil {
			return err
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO event_log (event_id,organization_id,session_id,type,payload,created_at) VALUES (?,?,?,?,?,?)`, event.EventID, event.OrganizationID, event.SessionID, event.Type, event.Payload, event.OccurredAt); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

var ErrGatewayEventStale = errors.New("gateway event stale")
