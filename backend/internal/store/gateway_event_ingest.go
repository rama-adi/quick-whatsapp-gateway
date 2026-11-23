package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

// GatewayEvent is the API persistence boundary for one at-least-once event.
// The control adapter may acknowledge it only after Ingest returns nil.
type GatewayEvent struct {
	EventID, GatewayID, SessionID, OrganizationID, Type string
	ConnectionEpoch, AssignmentEpoch                    uint64
	// JournalSequence is the gateway-local journal position. It is stored as
	// provenance; ordering uses the API-assigned per-session session_seq.
	JournalSequence uint64
	Payload         []byte
	OccurredAt      int64
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
			`INSERT IGNORE INTO gateway_ingested_events (gateway_event_id,gateway_id,connection_epoch,session_id,assignment_epoch,organization_id,event_log_id,committed_at,journal_sequence) VALUES (?,?,?,?,?,?,?,?,?)`,
			event.EventID,
			event.GatewayID,
			event.ConnectionEpoch,
			event.SessionID,
			event.AssignmentEpoch,
			event.OrganizationID,
			event.EventID,
			committedAt,
			event.JournalSequence,
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
					// The API already owns this message's public event: the
					// echo is acknowledged and deduplicated, with no fan-out.
					continue
				}
			}
		}
		if r.MediaCapture != nil {
			event.Payload, err = r.MediaCapture(ctx, tx, event)
			if err != nil {
				return fmt.Errorf("store: capture gateway event %s (%s): %w", event.EventID, event.Type, err)
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
		// The dedupe row is pruned after a retention window; a replay older
		// than that window finds the event body already logged and is a no-op.
		var logged int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_log WHERE event_id=?`, event.EventID).Scan(&logged); err != nil {
			return fmt.Errorf("store: check event log %s: %w", event.EventID, err)
		}
		if logged > 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO event_log (event_id,organization_id,session_id,type,payload,created_at) VALUES (?,?,?,?,?,?)`,
			event.EventID, event.OrganizationID, event.SessionID, event.Type, event.Payload, event.OccurredAt,
		); err != nil {
			return err
		}
		if err = enqueueFanout(ctx, tx, fanoutItem{
			eventID: event.EventID, organizationID: event.OrganizationID, sessionID: event.SessionID,
			source: FanoutSourceGateway, createdAt: committedAt,
		}); err != nil {
			return err
		}
	}
	if err = commit(); err != nil {
		return err
	}
	return nil
}

var ErrGatewayEventStale = errors.New("gateway event stale")
