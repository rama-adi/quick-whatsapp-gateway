package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// GatewayAssignmentRepo is the sole mutation path for control-stream desired
// state. It deliberately uses one SQL transaction because a reassignment must
// fence the old owner, fence the new owner, and notify both snapshots together.
type GatewayAssignmentRepo struct{ db *sql.DB }

func NewGatewayAssignmentRepo(db *sql.DB) *GatewayAssignmentRepo {
	return &GatewayAssignmentRepo{db: db}
}

// Reassign moves a session to newGatewayID. It increments the durable epoch and
// both the old and new gateway revisions atomically. An equal owner is a no-op.
func (r *GatewayAssignmentRepo) Reassign(ctx context.Context, sessionID, newGatewayID string, at int64) (uint64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin session reassignment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var oldGatewayID string
	var epoch uint64
	if err := tx.QueryRowContext(ctx, `SELECT gateway_id, assignment_epoch FROM gateway_session_assignments WHERE session_id=? FOR UPDATE`, sessionID).Scan(&oldGatewayID, &epoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, domain.ErrNotFound("session assignment not found")
		}
		return 0, fmt.Errorf("store: lock session assignment: %w", err)
	}
	if oldGatewayID == newGatewayID {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("store: commit unchanged session assignment: %w", err)
		}
		return epoch, nil
	}
	var eligible int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateways WHERE id=? AND deleted_at IS NULL AND status NOT IN ('pending_enrollment','disabled')`, newGatewayID).Scan(&eligible); err != nil || eligible != 1 {
		return 0, fmt.Errorf("store: target gateway is not eligible")
	}
	if result, err := tx.ExecContext(ctx, `UPDATE gateway_session_assignments SET gateway_id=?, assignment_epoch=assignment_epoch+1, updated_at=? WHERE session_id=?`, newGatewayID, at, sessionID); err != nil {
		return 0, fmt.Errorf("store: update session assignment: %w", err)
	} else if n, _ := result.RowsAffected(); n != 1 {
		return 0, fmt.Errorf("store: assignment update lost")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wa_sessions SET gateway_id=?, updated_at=? WHERE id=?`, newGatewayID, at, sessionID); err != nil {
		return 0, fmt.Errorf("store: update session gateway pin: %w", err)
	}
	if result, err := tx.ExecContext(ctx, `UPDATE gateways SET desired_revision=desired_revision+1, updated_at=? WHERE id IN (?, ?) AND deleted_at IS NULL`, at, oldGatewayID, newGatewayID); err != nil {
		return 0, fmt.Errorf("store: advance assignment revisions: %w", err)
	} else if n, _ := result.RowsAffected(); n != 2 {
		return 0, fmt.Errorf("store: assignment revision update lost")
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit session reassignment: %w", err)
	}
	return epoch + 1, nil
}

// UpdateConfig updates a session's streamed configuration and advances exactly
// its currently assigned gateway's desired revision in the same transaction.
func (r *GatewayAssignmentRepo) UpdateConfig(ctx context.Context, sessionID string, autoRead, presenceTyping bool, ratePerMin, ratePerHour int32, at int64) error {
	if ratePerMin < 0 || ratePerHour < 0 {
		return fmt.Errorf("store: session rates must be nonnegative")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin session config update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var gatewayID string
	if err := tx.QueryRowContext(ctx, `SELECT gateway_id FROM gateway_session_assignments WHERE session_id=? FOR UPDATE`, sessionID).Scan(&gatewayID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound("session assignment not found")
		}
		return fmt.Errorf("store: lock session configuration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wa_sessions SET auto_read=?, presence_typing=?, rate_per_min=?, rate_per_hour=?, updated_at=? WHERE id=?`, autoRead, presenceTyping, ratePerMin, ratePerHour, at, sessionID); err != nil {
		return fmt.Errorf("store: update session configuration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gateways SET desired_revision=desired_revision+1, updated_at=? WHERE id=? AND deleted_at IS NULL`, at, gatewayID); err != nil {
		return fmt.Errorf("store: advance config revision: %w", err)
	}
	return tx.Commit()
}

// SetSessionDesired flips one session's desired run state and advances the
// owning gateway's desired revision in one transaction, so the next desired
// state push starts or stops the session on the assigned gateway. It is the
// API-local replacement for direct gateway Start/Stop RPCs (gRPC Increment 7).
func (r *GatewayAssignmentRepo) SetSessionDesired(ctx context.Context, sessionID string, run bool, at int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin session desired update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var gatewayID string
	if err := tx.QueryRowContext(ctx, `SELECT gateway_id FROM gateway_session_assignments WHERE session_id=? FOR UPDATE`, sessionID).Scan(&gatewayID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound("session assignment not found")
		}
		return fmt.Errorf("store: lock session assignment: %w", err)
	}
	status := storedb.WaSessionsStatusStopped
	if run {
		status = storedb.WaSessionsStatusStarting
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wa_sessions SET status=?, updated_at=? WHERE id=?`, status, at, sessionID); err != nil {
		return fmt.Errorf("store: update session desired state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gateways SET desired_revision=desired_revision+1, updated_at=? WHERE id=? AND deleted_at IS NULL`, at, gatewayID); err != nil {
		return fmt.Errorf("store: advance desired revision: %w", err)
	}
	return tx.Commit()
}
