package store

import (
	"context"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// CreateSession commits the row and its first assignment together. An assignment
// failure must not leave a visible session that no gateway can operate.
func (r *GatewayAssignmentRepo) CreateSession(ctx context.Context, session domain.WASession) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin session creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := NewSessionRepo(tx).Create(ctx, session); err != nil {
		return err
	}
	if _, err := assignSession(ctx, tx, session.ID, session.GatewayID, session.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteSession removes the event work ledger before its FK parent, and advances
// the gateway revision before the assignment disappears through ON DELETE CASCADE.
// The durable event log is retained for existing webhook deliveries and retention.
func (r *GatewayAssignmentRepo) DeleteSession(ctx context.Context, sessionID string, at int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin session deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var gatewayID string
	if err := tx.QueryRowContext(ctx,
		`SELECT gateway_id FROM wa_sessions WHERE id=? FOR UPDATE`, sessionID,
	).Scan(&gatewayID); err != nil {
		return notFound(err, "session")
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM gateway_ingested_events WHERE session_id=?`, sessionID,
	); err != nil {
		return fmt.Errorf("store: remove deleted session event work: %w", err)
	}
	if err := NewSessionRepo(tx).Delete(ctx, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE gateways SET desired_revision=desired_revision+1, updated_at=? WHERE id=? AND deleted_at IS NULL`,
		at, gatewayID,
	); err != nil {
		return fmt.Errorf("store: advance deleted session revision: %w", err)
	}
	return tx.Commit()
}
