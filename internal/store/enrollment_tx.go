package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

type EnrollmentStore struct{ db *sql.DB }

const (
	tokenStatusActive    = "active"
	tokenStatusRedeeming = "redeeming"
	tokenStatusConsumed  = "consumed"
	tokenStatusLocked    = "locked"
	tokenStatusRevoked   = "revoked"

	gatewayStatusPendingEnrollment = "pending_enrollment"
	gatewayStatusJoining           = "joining"
	gatewayStatusActive            = "active"
	gatewayStatusDraining          = "draining"
	gatewayStatusDrained           = "drained"

	dispositionReplay      = "replay"
	dispositionInvalid     = "invalid"
	dispositionRateLimited = "rate_limited"
	dispositionInProgress  = "in_progress"
	dispositionAcquired    = "acquired"
)

func NewEnrollmentStore(db *sql.DB) *EnrollmentStore { return &EnrollmentStore{db: db} }

type SelectorRecord struct {
	TokenID, GatewayID, Status string
	TokenHash                  []byte
}

type AcquireEnrollmentLeaseInput struct {
	GatewayID, TokenID string
	ExpectedHash       []byte
	CSRHash, Nonce     []byte
	Now, LeaseUntil    int64
	CSRValid           bool
	StartedAudit       domain.AuditEvent
}

type AcquireEnrollmentLeaseResult struct {
	Disposition string
	Certificate *domain.GatewayCertificate
}

type FinalizeEnrollmentIssuanceInput struct {
	GatewayID, TokenID string
	ExpectedHash       []byte
	Nonce, CSRHash     []byte
	Now                int64
	Certificate        domain.GatewayCertificate
	SucceededAudit     domain.AuditEvent
}

type ReleaseEnrollmentLeaseInput struct {
	GatewayID, TokenID string
	ExpectedHash       []byte
	Nonce, CSRHash     []byte
	Now                int64
	FailureAudit       domain.AuditEvent
}

type RecoverEnrollmentIssuanceInput struct {
	GatewayID, TokenID string
	ExpectedHash       []byte
	CSRHash            []byte
	Now                int64
}

func (s *EnrollmentStore) LookupSelector(ctx context.Context, id string) (SelectorRecord, error) {
	var r SelectorRecord
	err := s.db.QueryRowContext(ctx, "SELECT id,gateway_id,status,token_hash FROM gateway_enrollment_tokens WHERE id=?", id).Scan(&r.TokenID, &r.GatewayID, &r.Status, &r.TokenHash)
	return r, err
}

func (s *EnrollmentStore) CreatePendingWithToken(ctx context.Context, g domain.Gateway, notes *string, creator string, token domain.EnrollmentToken, audits []domain.AuditEvent) error {
	return s.withTx(ctx, func(t *enrollmentTx) error {
		if err := NewGatewayRepo(t.tx).CreatePending(ctx, g, notes, &creator, 0); err != nil {
			return err
		}
		if err := newEnrollmentTokenRepo(t.tx).issue(ctx, token); err != nil {
			return err
		}
		for _, event := range audits {
			if err := NewAuditRepo(t.tx).Append(ctx, event); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *EnrollmentStore) ReplaceLiveToken(ctx context.Context, gatewayID string, token domain.EnrollmentToken, audit domain.AuditEvent, now int64) error {
	err := s.withGatewayTx(ctx, gatewayID, func(t *enrollmentTx) error {
		if !t.gatewayPending() {
			return ErrEnrollmentState
		}
		if _, err := newEnrollmentTokenRepo(t.tx).revokeLiveByGateway(ctx, gatewayID, now); err != nil {
			return err
		}
		if err := newEnrollmentTokenRepo(t.tx).issue(ctx, token); err != nil {
			return err
		}
		return NewAuditRepo(t.tx).Append(ctx, audit)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEnrollmentState
	}
	return err
}

func (s *EnrollmentStore) AcquireEnrollmentLease(ctx context.Context, in AcquireEnrollmentLeaseInput) (AcquireEnrollmentLeaseResult, error) {
	var out AcquireEnrollmentLeaseResult
	err := s.withGatewayTx(ctx, in.GatewayID, func(t *enrollmentTx) error {
		token, err := t.loadToken(ctx, in.TokenID, in.ExpectedHash)
		if err != nil {
			return ErrEnrollmentState
		}
		if token.Status == tokenStatusConsumed {
			if !t.gatewayRecoveryEligible() {
				return ErrEnrollmentState
			}
			cert, err := newGatewayCertificateRepo(t.tx).getByTokenCSR(ctx, in.TokenID, in.CSRHash)
			if err != nil || !certificateMatches(cert, in.GatewayID, in.TokenID, in.CSRHash, in.Now) {
				return ErrEnrollmentState
			}
			out.Disposition, out.Certificate = dispositionReplay, &cert
			return nil
		}
		if token.ExpiresAt <= in.Now || token.Status == tokenStatusRevoked {
			return ErrEnrollmentState
		}
		if token.Status == tokenStatusLocked {
			out.Disposition = dispositionRateLimited
			return nil
		}
		if token.AttemptCount >= token.MaxAttempts {
			if _, err = newEnrollmentTokenRepo(t.tx).lock(ctx, in.TokenID, in.Now); err != nil {
				return err
			}
			out.Disposition = dispositionRateLimited
			return nil
		}
		if token.Status == tokenStatusRedeeming {
			if token.LeaseExpiresAt == nil || !equalBytes(token.CSRSHA256, in.CSRHash) {
				return ErrEnrollmentState
			}
			if *token.LeaseExpiresAt > in.Now {
				out.Disposition = dispositionInProgress
				return nil
			}
		}
		if !in.CSRValid {
			lock := token.AttemptCount+1 >= token.MaxAttempts
			status := tokenStatusActive
			if lock {
				status = tokenStatusLocked
			}
			if _, err = t.tx.ExecContext(ctx, "UPDATE gateway_enrollment_tokens SET status=?,attempt_count=LEAST(attempt_count+1,max_attempts),redemption_nonce=NULL,csr_sha256=NULL,redeeming_at=NULL,lease_expires_at=NULL,updated_at=? WHERE id=?", status, in.Now, in.TokenID); err != nil {
				return err
			}
			if lock {
				out.Disposition = dispositionRateLimited
			} else {
				out.Disposition = dispositionInvalid
			}
			return nil
		}
		ok, err := newEnrollmentTokenRepo(t.tx).begin(ctx, in.TokenID, in.Nonce, in.CSRHash, in.Now, in.LeaseUntil)
		if err != nil {
			return err
		}
		if !ok {
			return ErrEnrollmentState
		}
		if err = NewAuditRepo(t.tx).Append(ctx, in.StartedAudit); err != nil {
			return err
		}
		out.Disposition = dispositionAcquired
		return nil
	})
	return out, err
}

func (s *EnrollmentStore) FinalizeEnrollmentIssuance(ctx context.Context, in FinalizeEnrollmentIssuanceInput) (domain.GatewayCertificate, error) {
	out := in.Certificate
	err := s.withGatewayTx(ctx, in.GatewayID, func(t *enrollmentTx) error {
		token, err := t.loadToken(ctx, in.TokenID, in.ExpectedHash)
		if err != nil || token.Status != tokenStatusRedeeming || !equalBytes(token.RedemptionNonce, in.Nonce) || !equalBytes(token.CSRSHA256, in.CSRHash) || token.LeaseExpiresAt == nil || *token.LeaseExpiresAt <= in.Now {
			return ErrEnrollmentState
		}
		duplicateIssuance := false
		if err = newGatewayCertificateRepo(t.tx).insert(ctx, in.Certificate); err != nil {
			persisted, lookupErr := newGatewayCertificateRepo(t.tx).getByTokenCSR(ctx, in.TokenID, in.CSRHash)
			if lookupErr != nil {
				return err
			}
			out = persisted
			duplicateIssuance = true
		}
		if t.gatewayPending() {
			ok, markErr := NewGatewayRepo(t.tx).markEnrolledJoining(ctx, in.GatewayID, in.Now)
			if markErr != nil {
				return markErr
			}
			if !ok {
				return ErrEnrollmentState
			}
		} else if !t.gatewayDuplicateFinalizeEligible(duplicateIssuance) {
			return ErrEnrollmentState
		}
		ok, err := newEnrollmentTokenRepo(t.tx).finalize(ctx, in.TokenID, in.Nonce, in.CSRHash, in.Now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrEnrollmentState
		}
		return NewAuditRepo(t.tx).Append(ctx, in.SucceededAudit)
	})
	return out, err
}

func (s *EnrollmentStore) ReleaseEnrollmentLease(ctx context.Context, in ReleaseEnrollmentLeaseInput) (bool, error) {
	released := false
	err := s.withGatewayTx(ctx, in.GatewayID, func(t *enrollmentTx) error {
		token, err := t.loadToken(ctx, in.TokenID, in.ExpectedHash)
		if err != nil || token.Status != tokenStatusRedeeming || !equalBytes(token.RedemptionNonce, in.Nonce) || !equalBytes(token.CSRSHA256, in.CSRHash) {
			return nil
		}
		ok, err := newEnrollmentTokenRepo(t.tx).release(ctx, in.TokenID, in.Nonce, in.CSRHash, in.Now)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err = NewAuditRepo(t.tx).Append(ctx, in.FailureAudit); err != nil {
			return err
		}
		released = true
		return nil
	})
	return released, err
}

func (s *EnrollmentStore) RecoverEnrollmentIssuance(ctx context.Context, in RecoverEnrollmentIssuanceInput) (domain.GatewayCertificate, error) {
	var out domain.GatewayCertificate
	err := s.withGatewayTx(ctx, in.GatewayID, func(t *enrollmentTx) error {
		token, err := t.loadToken(ctx, in.TokenID, in.ExpectedHash)
		if err != nil || token.Status != tokenStatusConsumed || !t.gatewayRecoveryEligible() {
			return ErrEnrollmentState
		}
		out, err = newGatewayCertificateRepo(t.tx).getByTokenCSR(ctx, in.TokenID, in.CSRHash)
		if err != nil || !certificateMatches(out, in.GatewayID, in.TokenID, in.CSRHash, in.Now) {
			return ErrEnrollmentState
		}
		return nil
	})
	return out, err
}

var ErrEnrollmentState = errors.New("enrollment state mismatch")

type enrollmentTx struct {
	tx                       *sql.Tx
	gatewayID, gatewayStatus string
	gatewayDeleted           bool
	token                    domain.EnrollmentToken
	tokenLocked              bool
}

func (s *EnrollmentStore) withTx(ctx context.Context, fn func(*enrollmentTx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = fn(&enrollmentTx{tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *EnrollmentStore) withGatewayTx(ctx context.Context, gatewayID string, fn func(*enrollmentTx) error) error {
	return s.withTx(ctx, func(t *enrollmentTx) error {
		if err := t.loadGateway(ctx, gatewayID); err != nil {
			return err
		}
		return fn(t)
	})
}

func (t *enrollmentTx) loadGateway(ctx context.Context, id string) error {
	var status string
	var deleted sql.NullInt64
	if err := t.tx.QueryRowContext(ctx, "SELECT id,status,deleted_at FROM gateways WHERE id=? FOR UPDATE", id).Scan(&t.gatewayID, &status, &deleted); err != nil {
		return err
	}
	t.gatewayStatus = status
	t.gatewayDeleted = deleted.Valid
	return nil
}
func (t *enrollmentTx) gatewayPending() bool {
	return t.gatewayStatus == gatewayStatusPendingEnrollment && !t.gatewayDeleted
}
func (t *enrollmentTx) gatewayRecoveryEligible() bool {
	if t.gatewayDeleted {
		return false
	}
	switch t.gatewayStatus {
	case gatewayStatusJoining, gatewayStatusActive, gatewayStatusDraining, gatewayStatusDrained:
		return true
	default:
		return false
	}
}
func (t *enrollmentTx) gatewayDuplicateFinalizeEligible(duplicateIssuance bool) bool {
	return duplicateIssuance && !t.gatewayDeleted && t.gatewayStatus == gatewayStatusJoining
}
func certificateMatches(cert domain.GatewayCertificate, gatewayID, tokenID string, csrHash []byte, now int64) bool {
	return cert.GatewayID == gatewayID && cert.EnrollmentTokenID == tokenID && equalBytes(cert.CSRSHA256, csrHash) && cert.NotBefore <= now && cert.NotAfter > now
}
func (t *enrollmentTx) loadToken(ctx context.Context, id string, hash []byte) (domain.EnrollmentToken, error) {
	token, err := t.lockToken(ctx, id)
	if err != nil || !t.reverify(hash) {
		return token, ErrEnrollmentState
	}
	return token, nil
}
func (t *enrollmentTx) lockToken(ctx context.Context, id string) (domain.EnrollmentToken, error) {
	if t.tokenLocked {
		return domain.EnrollmentToken{}, errors.New("token already locked")
	}
	token, err := newEnrollmentTokenRepo(t.tx).getForUpdate(ctx, id)
	if err != nil {
		return token, err
	}
	if token.GatewayID != t.gatewayID {
		return token, ErrEnrollmentState
	}
	t.token, t.tokenLocked = token, true
	return token, nil
}
func (t *enrollmentTx) reverify(hash []byte) bool {
	return t.tokenLocked && len(hash) == len(t.token.TokenHash) && subtle.ConstantTimeCompare(hash, t.token.TokenHash) == 1
}
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}
