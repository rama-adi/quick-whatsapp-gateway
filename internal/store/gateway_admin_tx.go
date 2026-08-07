package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// ErrGatewayAdminState means that the requested administrative transition is
// not valid for the gateway's durable state. Callers deliberately receive no
// distinction between a missing and an ineligible gateway.
var ErrGatewayAdminState = errors.New("gateway administration state mismatch")

// GatewayAdminStore owns the transactional, operator-originated gateway state
// transitions. It uses small local statements rather than generated queries so
// a lifecycle change, its credential invalidation, and its audit row cannot be
// committed separately.
type GatewayAdminStore struct{ db *sql.DB }

func NewGatewayAdminStore(db *sql.DB) *GatewayAdminStore { return &GatewayAdminStore{db: db} }

type GatewayAdminAudit struct {
	Event domain.AuditEvent
}

func (s *GatewayAdminStore) Drain(ctx context.Context, gatewayID string, at int64, audit GatewayAdminAudit) error {
	return s.transition(ctx, gatewayID, at, audit, func(tx *sql.Tx, status string, enrolled bool) error {
		if !enrolled || (status != string(domain.GatewayJoining) && status != string(domain.GatewayActive) && status != string(domain.GatewayDegraded)) {
			return ErrGatewayAdminState
		}
		result, err := tx.ExecContext(ctx, "UPDATE gateways SET desired_lifecycle='drain', desired_revision=desired_revision+1, updated_at=? WHERE id=? AND deleted_at IS NULL AND status<> 'disabled'", at, gatewayID)
		return changed(result, err)
	})
}

func (s *GatewayAdminStore) Resume(ctx context.Context, gatewayID string, at int64, audit GatewayAdminAudit) error {
	return s.transition(ctx, gatewayID, at, audit, func(tx *sql.Tx, status string, enrolled bool) error {
		if !enrolled || (status != string(domain.GatewayDraining) && status != string(domain.GatewayDrained)) {
			return ErrGatewayAdminState
		}
		result, err := tx.ExecContext(ctx, "UPDATE gateways SET desired_lifecycle='run', desired_revision=desired_revision+1, updated_at=? WHERE id=? AND deleted_at IS NULL AND status<> 'disabled'", at, gatewayID)
		return changed(result, err)
	})
}

func (s *GatewayAdminStore) Disable(ctx context.Context, gatewayID string, at int64, audit GatewayAdminAudit) error {
	return s.transition(ctx, gatewayID, at, audit, func(tx *sql.Tx, status string, _ bool) error {
		if status == string(domain.GatewayDisabled) {
			return ErrGatewayAdminState
		}
		result, err := tx.ExecContext(ctx, "UPDATE gateways SET status='disabled', connection_epoch=connection_epoch+1, updated_at=? WHERE id=? AND deleted_at IS NULL", at, gatewayID)
		return changed(result, err)
	})
}

func (s *GatewayAdminStore) Reenable(ctx context.Context, gatewayID string, at int64, audit GatewayAdminAudit) error {
	return s.transition(ctx, gatewayID, at, audit, func(tx *sql.Tx, status string, enrolled bool) error {
		if status != string(domain.GatewayDisabled) {
			return ErrGatewayAdminState
		}
		next := "pending_enrollment"
		if enrolled {
			next = "joining"
		}
		result, err := tx.ExecContext(ctx, "UPDATE gateways SET status=?, desired_lifecycle='run', desired_revision=desired_revision+1, updated_at=? WHERE id=? AND deleted_at IS NULL AND status='disabled'", next, at, gatewayID)
		return changed(result, err)
	})
}

// Reenroll revokes credentials before returning a replacement bootstrap token.
// It is restricted to quiesced gateways, avoiding a surprise credential cutover
// on a gateway which can still be serving sessions.
func (s *GatewayAdminStore) Reenroll(ctx context.Context, gatewayID string, token domain.EnrollmentToken, at int64, audits []GatewayAdminAudit) error {
	return s.withGateway(ctx, gatewayID, func(tx *sql.Tx, status string, enrolled bool) error {
		if !enrolled || (status != string(domain.GatewayDrained) && status != string(domain.GatewayDisabled)) {
			return ErrGatewayAdminState
		}
		if _, err := tx.ExecContext(ctx, "UPDATE gateway_enrollment_tokens SET status='revoked', revoked_at=?, updated_at=?, redemption_nonce=NULL, csr_sha256=NULL, redeeming_at=NULL, lease_expires_at=NULL WHERE gateway_id=? AND status IN ('active','redeeming')", at, at, gatewayID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE gateway_certificates SET revoked_at=?, revocation_reason='re_enrollment' WHERE gateway_id=? AND revoked_at IS NULL", at, gatewayID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE gateways SET status='pending_enrollment', desired_lifecycle='run', desired_revision=desired_revision+1, connection_epoch=connection_epoch+1, connected_at=NULL, last_seen_at=NULL, updated_at=? WHERE id=? AND deleted_at IS NULL", at, gatewayID); err != nil {
			return err
		}
		if err := newEnrollmentTokenRepo(tx).issue(ctx, token); err != nil {
			return err
		}
		for _, audit := range audits {
			if err := NewAuditRepo(tx).Append(ctx, audit.Event); err != nil {
				return err
			}
		}
		return nil
	})
}

// Delete is intentionally a soft delete. It accepts only a terminal gateway
// with no assigned sessions, unredeemed bootstrap token, or usable certificate.
func (s *GatewayAdminStore) Delete(ctx context.Context, gatewayID string, acknowledged bool, at int64, audit GatewayAdminAudit) error {
	if !acknowledged {
		return ErrGatewayAdminState
	}
	return s.withGateway(ctx, gatewayID, func(tx *sql.Tx, status string, _ bool) error {
		if status != string(domain.GatewayDrained) && status != string(domain.GatewayDisabled) {
			return ErrGatewayAdminState
		}
		var assigned int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM wa_sessions WHERE gateway_id=?", gatewayID).Scan(&assigned); err != nil {
			return err
		}
		if assigned != 0 {
			return ErrGatewayAdminState
		}
		result, err := tx.ExecContext(ctx, "UPDATE gateways SET status='disabled', deleted_at=?, updated_at=? WHERE id=? AND deleted_at IS NULL AND status IN ('drained','disabled') AND NOT EXISTS (SELECT 1 FROM gateway_enrollment_tokens t WHERE t.gateway_id=gateways.id AND t.status IN ('active','redeeming')) AND NOT EXISTS (SELECT 1 FROM gateway_certificates c WHERE c.gateway_id=gateways.id AND c.revoked_at IS NULL)", at, at, gatewayID)
		if err := changed(result, err); err != nil {
			return err
		}
		return NewAuditRepo(tx).Append(ctx, audit.Event)
	})
}

func (s *GatewayAdminStore) transition(ctx context.Context, gatewayID string, at int64, audit GatewayAdminAudit, apply func(*sql.Tx, string, bool) error) error {
	return s.withGateway(ctx, gatewayID, func(tx *sql.Tx, status string, enrolled bool) error {
		if err := apply(tx, status, enrolled); err != nil {
			return err
		}
		return NewAuditRepo(tx).Append(ctx, audit.Event)
	})
}

func (s *GatewayAdminStore) withGateway(ctx context.Context, gatewayID string, fn func(*sql.Tx, string, bool) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	var enrolledAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT status,enrolled_at FROM gateways WHERE id=? AND deleted_at IS NULL FOR UPDATE", gatewayID).Scan(&status, &enrolledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrGatewayAdminState
		}
		return err
	}
	if err := fn(tx, status, enrolledAt.Valid); err != nil {
		return err
	}
	return tx.Commit()
}

func changed(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrGatewayAdminState
	}
	return nil
}
