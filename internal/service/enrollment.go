package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/enrollmenttoken"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

var ErrEnrollmentDenied = errors.New("enrollment denied")

type InvalidCredentialError struct{ Cause error }

func (e *InvalidCredentialError) Error() string { return "invalid enrollment credential" }
func (e *InvalidCredentialError) Unwrap() error { return e.Cause }

type InProgressError struct{ Cause error }

func (e *InProgressError) Error() string { return "enrollment in progress" }
func (e *InProgressError) Unwrap() error { return e.Cause }

type RateLimitedError struct{ Cause error }

func (e *RateLimitedError) Error() string { return "enrollment rate limited" }
func (e *RateLimitedError) Unwrap() error { return e.Cause }

type TransientError struct{ Cause error }

func (e *TransientError) Error() string { return "enrollment temporarily unavailable" }
func (e *TransientError) Unwrap() error { return e.Cause }

type StateConflictError struct{ Cause error }

func (e *StateConflictError) Error() string { return "enrollment state conflict" }
func (e *StateConflictError) Unwrap() error { return e.Cause }

const (
	actorTypeUser   = "user"
	actorTypeNode   = "gateway"
	fallbackTokenID = "00000000000000000000000000"
	tokenPrefixLen  = 16

	enrollmentDispositionReplay     = "replay"
	enrollmentDispositionInvalid    = "invalid"
	enrollmentDispositionLimited    = "rate_limited"
	enrollmentDispositionProgress   = "in_progress"
	enrollmentDispositionAcquired   = "acquired"
	enrollmentFailureLeaseExhausted = "lease_exhausted"
	enrollmentFailureSignFailure    = "sign_failure"
	enrollmentFailureSignerInvalid  = "invalid_signer_output"
	enrollmentFailureFinalize       = "finalize_failure"
)

var enrollmentTokenVerify = enrollmenttoken.Verify

type EnrollmentConfig struct {
	TokenTTL, LeaseTTL, SignTimeout, SafetyMargin time.Duration
	MaxAttempts                                   uint32
}

type presentedEnrollment struct {
	tokenID    string
	gatewayID  string
	expected   [32]byte
	credential bool
}

func DefaultEnrollmentConfig() EnrollmentConfig {
	return EnrollmentConfig{TokenTTL: 15 * time.Minute, LeaseTTL: 60 * time.Second, SignTimeout: 20 * time.Second, SafetyMargin: 5 * time.Second, MaxAttempts: 5}
}
func (c EnrollmentConfig) Validate() error {
	if c.TokenTTL <= 0 || c.LeaseTTL <= 0 || c.SignTimeout <= 0 || c.SignTimeout >= c.LeaseTTL || c.SafetyMargin <= 0 || c.SignTimeout+c.SafetyMargin >= c.LeaseTTL || c.MaxAttempts == 0 {
		return errors.New("invalid enrollment config")
	}
	return nil
}

type IDGenerator func() string
type EnrollmentDependencies struct {
	Clock       func() time.Time
	Entropy     io.Reader
	IDs         IDGenerator
	DenialDelay func(context.Context, time.Time)
	Store       EnrollmentPersistence
}
type EnrollmentPersistence interface {
	CreatePendingWithToken(context.Context, domain.Gateway, *string, string, domain.EnrollmentToken, []domain.AuditEvent) error
	LookupSelector(context.Context, string) (store.SelectorRecord, error)
	ReplaceLiveToken(context.Context, string, domain.EnrollmentToken, domain.AuditEvent, int64) error
	AcquireEnrollmentLease(context.Context, store.AcquireEnrollmentLeaseInput) (store.AcquireEnrollmentLeaseResult, error)
	FinalizeEnrollmentIssuance(context.Context, store.FinalizeEnrollmentIssuanceInput) (domain.GatewayCertificate, error)
	ReleaseEnrollmentLease(context.Context, store.ReleaseEnrollmentLeaseInput) (bool, error)
	RecoverEnrollmentIssuance(context.Context, store.RecoverEnrollmentIssuanceInput) (domain.GatewayCertificate, error)
}
type EnrollmentService struct {
	store       EnrollmentPersistence
	signer      pki.CertificateSigner
	cfg         EnrollmentConfig
	now         func() time.Time
	entropy     io.Reader
	id          IDGenerator
	denialDelay func(context.Context, time.Time)
}

func NewEnrollmentService(db *sql.DB, signer pki.CertificateSigner, cfg EnrollmentConfig) (*EnrollmentService, error) {
	return NewEnrollmentServiceWithDependencies(db, signer, cfg, EnrollmentDependencies{Clock: time.Now, Entropy: rand.Reader, IDs: func() string { return ulid.Make().String() }, DenialDelay: defaultDenialDelay})
}
func defaultDenialDelay(ctx context.Context, started time.Time) {
	var b [1]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	d := time.Until(started.Add(5*time.Millisecond + time.Duration(b[0]%11)*time.Millisecond))
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
func NewEnrollmentServiceWithDependencies(db *sql.DB, signer pki.CertificateSigner, cfg EnrollmentConfig, deps EnrollmentDependencies) (*EnrollmentService, error) {
	if (db == nil && deps.Store == nil) || signer == nil {
		return nil, errors.New("enrollment dependencies required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if deps.Clock == nil || deps.Entropy == nil || deps.IDs == nil || deps.DenialDelay == nil {
		return nil, errors.New("enrollment injected dependencies required")
	}
	persistence := deps.Store
	if persistence == nil {
		persistence = store.NewEnrollmentStore(db)
	}
	return &EnrollmentService{store: persistence, signer: signer, cfg: cfg, now: deps.Clock, entropy: deps.Entropy, id: deps.IDs, denialDelay: deps.DenialDelay}, nil
}

type CreateGatewayInput struct {
	Label, Notes    *string
	Capacity        *int
	CreatedByUserID string
	Actor           Actor
}
type Actor struct{ Type, ID, RequestID string }
type IssuedEnrollment struct {
	GatewayID, TokenID, Token string
	ExpiresAt                 int64
}
type EnrollmentResult struct {
	GatewayID, CertificatePEM, TrustBundlePEM, AuthorityID, SerialNumber string
	NotBefore, NotAfter                                                  int64
}

func (s *EnrollmentService) CreateGateway(ctx context.Context, in CreateGatewayInput) (IssuedEnrollment, error) {
	actor := s.actorOrCreator(in.Actor, in.CreatedByUserID)
	if !s.isUserActor(actor) {
		return IssuedEnrollment{}, ErrEnrollmentDenied
	}
	now := s.now().UTC()
	gatewayID := "gw_" + s.id()
	token, persisted, err := s.newEnrollmentToken(now, gatewayID, actor.ID)
	if err != nil {
		return IssuedEnrollment{}, err
	}
	audits := []domain.AuditEvent{
		s.audit(actor, "gateway.created", gatewayID, "success", now.UnixMilli(), nil),
		s.audit(actor, "enrollment.issued", gatewayID, "success", now.UnixMilli(), nil),
	}
	err = s.store.CreatePendingWithToken(ctx, s.newGateway(now, gatewayID, in), in.Notes, actor.ID, persisted, audits)
	if err != nil {
		return IssuedEnrollment{}, err
	}
	return IssuedEnrollment{gatewayID, token.Selector(), token.String(), persisted.ExpiresAt}, nil
}
func (s *EnrollmentService) ReplaceToken(ctx context.Context, gatewayID, userID string) (IssuedEnrollment, error) {
	return s.ReplaceTokenAs(ctx, gatewayID, Actor{Type: actorTypeUser, ID: userID})
}
func (s *EnrollmentService) ReplaceTokenAs(ctx context.Context, gatewayID string, actor Actor) (IssuedEnrollment, error) {
	userID := actor.ID
	if gatewayID == "" || userID == "" || actor.Type != actorTypeUser {
		return IssuedEnrollment{}, ErrEnrollmentDenied
	}
	now := s.now().UTC()
	token, persisted, err := s.newEnrollmentToken(now, gatewayID, userID)
	if err != nil {
		return IssuedEnrollment{}, err
	}
	err = s.store.ReplaceLiveToken(ctx, gatewayID, persisted, s.audit(actor, "enrollment.replaced", gatewayID, "success", now.UnixMilli(), nil), now.UnixMilli())
	if err != nil {
		return IssuedEnrollment{}, publicErr(err)
	}
	return IssuedEnrollment{gatewayID, token.Selector(), token.String(), persisted.ExpiresAt}, nil
}

func (s *EnrollmentService) actorOrCreator(actor Actor, createdBy string) Actor {
	if actor.ID != "" {
		return actor
	}
	return Actor{Type: actorTypeUser, ID: createdBy}
}

func (s *EnrollmentService) isUserActor(actor Actor) bool {
	return actor.Type == actorTypeUser && actor.ID != ""
}

func (s *EnrollmentService) newEnrollmentToken(now time.Time, gatewayID, actorID string) (enrollmenttoken.Token, domain.EnrollmentToken, error) {
	token, err := enrollmenttoken.GenerateWith(s.entropy, s.id)
	if err != nil {
		return enrollmenttoken.Token{}, domain.EnrollmentToken{}, err
	}
	expires := now.Add(s.cfg.TokenTTL).UnixMilli()
	tokenHash := enrollmenttoken.Digest(token.String())
	return token, domain.EnrollmentToken{
		ID:              token.Selector(),
		GatewayID:       gatewayID,
		TokenHash:       tokenHash[:],
		TokenPrefix:     safePrefix(token.String()),
		MaxAttempts:     s.cfg.MaxAttempts,
		ExpiresAt:       expires,
		CreatedByUserID: actorID,
		CreatedAt:       now.UnixMilli(),
		UpdatedAt:       now.UnixMilli(),
	}, nil
}

func (s *EnrollmentService) newGateway(now time.Time, gatewayID string, in CreateGatewayInput) domain.Gateway {
	return domain.Gateway{ID: gatewayID, Label: in.Label, Capacity: in.Capacity, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()}
}

func (s *EnrollmentService) resolveCredential(ctx context.Context, token string) presentedEnrollment {
	parsed, parseErr := enrollmenttoken.Parse(token)
	tokenID := fallbackTokenID
	if parseErr == nil {
		tokenID = parsed.Selector()
	}
	lookup, err := s.store.LookupSelector(ctx, tokenID)
	var expected [32]byte
	if err == nil {
		copy(expected[:], lookup.TokenHash)
	}
	if !enrollmentTokenVerify(token, expected) {
		return presentedEnrollment{tokenID: tokenID}
	}
	if parseErr != nil || err != nil {
		return presentedEnrollment{tokenID: tokenID}
	}
	return presentedEnrollment{
		tokenID:    tokenID,
		gatewayID:  lookup.GatewayID,
		expected:   expected,
		credential: true,
	}
}

type RedeemInput struct {
	Token     string
	CSRDER    []byte
	RequestID string
}

func (s *EnrollmentService) Redeem(ctx context.Context, presented string, csrDER []byte) (EnrollmentResult, error) {
	return s.RedeemWithInput(ctx, RedeemInput{Token: presented, CSRDER: csrDER})
}
func (s *EnrollmentService) RedeemWithInput(ctx context.Context, input RedeemInput) (EnrollmentResult, error) {
	requestStarted := s.now()
	credential := s.resolveCredential(ctx, input.Token)
	if !credential.credential {
		return EnrollmentResult{}, s.deniedAt(ctx, requestStarted)
	}
	validation := s.parseCSR(input.CSRDER, credential.gatewayID)
	nonce, err := s.nextNonce()
	if err != nil {
		return EnrollmentResult{}, err
	}
	now := s.now().UTC()
	acquired, err := s.startLease(ctx, input.RequestID, credential, nonce, now, validation)
	if err != nil {
		return EnrollmentResult{}, s.mapError(ctx, err, requestStarted)
	}
	disposition := s.handleLeaseDisposition(ctx, requestStarted, acquired)
	if disposition.done {
		return disposition.result, disposition.err
	}
	deadline, ok := s.calculateSigningDeadline(now, now.Add(s.cfg.LeaseTTL))
	if !ok {
		s.release(credential.gatewayID, credential.tokenID, credential.expected, nonce, validation.hash[:], enrollmentFailureLeaseExhausted, input.RequestID)
		return EnrollmentResult{}, &TransientError{Cause: context.DeadlineExceeded}
	}
	signed, err := s.signEnrollment(ctx, deadline, validation, credential)
	if err != nil {
		s.release(credential.gatewayID, credential.tokenID, credential.expected, nonce, validation.hash[:], enrollmentFailureSignFailure, input.RequestID)
		if ctx.Err() != nil {
			return EnrollmentResult{}, ctx.Err()
		}
		return EnrollmentResult{}, &TransientError{Cause: err}
	}
	if validationErr := pki.ValidateSignedGateway(signed, validation.validated, credential.gatewayID, s.now().UTC()); validationErr != nil {
		s.release(credential.gatewayID, credential.tokenID, credential.expected, nonce, validation.hash[:], enrollmentFailureSignerInvalid, input.RequestID)
		return EnrollmentResult{}, &TransientError{Cause: validationErr}
	}
	persisted, err := s.persistCertificate(ctx, requestStarted, credential, nonce, validation, input.RequestID, signed)
	if err != nil {
		return persisted, err
	}
	return persisted, nil
}

type parsedCSR struct {
	validated pki.ValidatedCSR
	hash      [32]byte
	valid     bool
}

func (s *EnrollmentService) parseCSR(csrDER []byte, gatewayID string) parsedCSR {
	rawHash := sha256.Sum256(csrDER)
	validated, validationErr := pki.ValidateCSR(csrDER, gatewayID)
	if validationErr != nil {
		return parsedCSR{hash: rawHash, valid: false}
	}
	return parsedCSR{validated: validated, hash: validated.DERHash(), valid: true}
}

func (s *EnrollmentService) nextNonce() ([]byte, error) {
	nonce := make([]byte, 16)
	_, err := io.ReadFull(s.entropy, nonce)
	return nonce, err
}

func (s *EnrollmentService) startLease(ctx context.Context, requestID string, credential presentedEnrollment, nonce []byte, now time.Time, csr parsedCSR) (store.AcquireEnrollmentLeaseResult, error) {
	input := store.AcquireEnrollmentLeaseInput{
		GatewayID:    credential.gatewayID,
		TokenID:      credential.tokenID,
		ExpectedHash: credential.expected[:],
		CSRHash:      csr.hash[:],
		Nonce:        nonce,
		Now:          now.UnixMilli(),
		LeaseUntil:   now.Add(s.cfg.LeaseTTL).UnixMilli(),
		CSRValid:     csr.valid,
		StartedAudit: s.audit(Actor{Type: actorTypeNode, ID: credential.gatewayID, RequestID: requestID}, "enrollment.started", credential.gatewayID, "success", now.UnixMilli(), nil),
	}
	return s.store.AcquireEnrollmentLease(ctx, input)
}

type leaseDispositionDecision struct {
	done   bool
	result EnrollmentResult
	err    error
}

func (s *EnrollmentService) handleLeaseDisposition(ctx context.Context, started time.Time, acquired store.AcquireEnrollmentLeaseResult) leaseDispositionDecision {
	switch acquired.Disposition {
	case enrollmentDispositionReplay:
		return leaseDispositionDecision{done: true, result: resultFromCertificate(*acquired.Certificate)}
	case enrollmentDispositionInvalid:
		return leaseDispositionDecision{done: true, err: s.deniedAt(ctx, started)}
	case enrollmentDispositionLimited:
		return leaseDispositionDecision{done: true, err: &RateLimitedError{Cause: ErrEnrollmentDenied}}
	case enrollmentDispositionProgress:
		return leaseDispositionDecision{done: true, err: &InProgressError{Cause: ErrEnrollmentDenied}}
	case enrollmentDispositionAcquired:
		return leaseDispositionDecision{done: false}
	default:
		return leaseDispositionDecision{done: true, err: &TransientError{Cause: errors.New("unknown enrollment disposition")}}
	}
}

func (s *EnrollmentService) calculateSigningDeadline(now, leaseUntil time.Time) (time.Time, bool) {
	deadline := now.Add(s.cfg.SignTimeout)
	leaseDeadline := leaseUntil.Add(-s.cfg.SafetyMargin)
	if leaseDeadline.Before(deadline) {
		deadline = leaseDeadline
	}
	if !deadline.After(s.now().UTC()) {
		return deadline, false
	}
	return deadline, true
}

func (s *EnrollmentService) signEnrollment(ctx context.Context, deadline time.Time, csr parsedCSR, credential presentedEnrollment) (pki.SignedCertificate, error) {
	signCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return s.signer.Sign(signCtx, pki.SignRequest{
		GatewayID:         credential.gatewayID,
		EnrollmentTokenID: credential.tokenID,
		CSR:               csr.validated,
	})
}

func (s *EnrollmentService) persistCertificate(ctx context.Context, started time.Time, credential presentedEnrollment, nonce []byte, csr parsedCSR, requestID string, signed pki.SignedCertificate) (EnrollmentResult, error) {
	result := EnrollmentResult{
		GatewayID:      credential.gatewayID,
		CertificatePEM: string(signed.ChainPEM),
		TrustBundlePEM: string(signed.TrustBundlePEM),
		AuthorityID:    signed.AuthorityID,
		SerialNumber:   signed.Serial.String(),
		NotBefore:      signed.NotBefore.UnixMilli(),
		NotAfter:       signed.NotAfter.UnixMilli(),
	}
	now := s.now().UTC().UnixMilli()
	cert := domain.GatewayCertificate{
		ID:                s.id(),
		GatewayID:         credential.gatewayID,
		AuthorityID:       signed.AuthorityID,
		EnrollmentTokenID: credential.tokenID,
		CSRSHA256:         csr.hash[:],
		SerialNumber:      signed.Serial.String(),
		CertificatePEM:    string(signed.ChainPEM),
		TrustBundlePEM:    string(signed.TrustBundlePEM),
		Fingerprint:       signed.Fingerprint,
		NotBefore:         result.NotBefore,
		NotAfter:          result.NotAfter,
		CreatedAt:         now,
	}
	persisted, err := s.store.FinalizeEnrollmentIssuance(ctx, store.FinalizeEnrollmentIssuanceInput{
		GatewayID:      credential.gatewayID,
		TokenID:        credential.tokenID,
		ExpectedHash:   credential.expected[:],
		Nonce:          nonce,
		CSRHash:        csr.hash[:],
		Now:            now,
		Certificate:    cert,
		SucceededAudit: s.audit(Actor{Type: actorTypeNode, ID: credential.gatewayID, RequestID: requestID}, "enrollment.succeeded", credential.gatewayID, "success", now, map[string]any{"certificate_fingerprint": fmt.Sprintf("%x", signed.Fingerprint)}),
	})
	if err != nil {
		recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer recoveryCancel()
		if recovered, re := s.recoverCommitted(recoveryCtx, credential.gatewayID, credential.tokenID, credential.expected, csr.hash[:]); re == nil {
			return recovered, nil
		}
		s.release(credential.gatewayID, credential.tokenID, credential.expected, nonce, csr.hash[:], enrollmentFailureFinalize, requestID)
		if ctx.Err() != nil {
			return EnrollmentResult{}, ctx.Err()
		}
		if errors.Is(err, store.ErrEnrollmentState) {
			return EnrollmentResult{}, s.deniedAt(ctx, started)
		}
		return EnrollmentResult{}, &TransientError{Cause: err}
	}
	return resultFromCertificate(persisted), nil
}

func (s *EnrollmentService) release(gatewayID, id string, expected [32]byte, nonce, csr []byte, class, requestID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	now := s.now().UTC().UnixMilli()
	_, _ = s.store.ReleaseEnrollmentLease(ctx, store.ReleaseEnrollmentLeaseInput{GatewayID: gatewayID, TokenID: id, ExpectedHash: expected[:], Nonce: nonce, CSRHash: csr, Now: now, FailureAudit: s.audit(Actor{Type: actorTypeNode, ID: gatewayID, RequestID: requestID}, "enrollment.failed", gatewayID, "failure", now, map[string]any{"failure_class": class})})
}
func (s *EnrollmentService) recoverCommitted(ctx context.Context, gatewayID, id string, expected [32]byte, csr []byte) (EnrollmentResult, error) {
	cert, err := s.store.RecoverEnrollmentIssuance(ctx, store.RecoverEnrollmentIssuanceInput{GatewayID: gatewayID, TokenID: id, ExpectedHash: expected[:], CSRHash: csr, Now: s.now().UTC().UnixMilli()})
	return resultFromCertificate(cert), err
}
func resultFromCertificate(c domain.GatewayCertificate) EnrollmentResult {
	return EnrollmentResult{GatewayID: c.GatewayID, CertificatePEM: c.CertificatePEM, TrustBundlePEM: c.TrustBundlePEM, AuthorityID: c.AuthorityID, SerialNumber: c.SerialNumber, NotBefore: c.NotBefore, NotAfter: c.NotAfter}
}
func (s *EnrollmentService) audit(actor Actor, action, resource, outcome string, at int64, meta map[string]any) domain.AuditEvent {
	rid := resource
	aid := actor.ID
	var request *string
	if actor.RequestID != "" {
		request = &actor.RequestID
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["gateway_id"] = resource
	return domain.AuditEvent{ID: s.id(), ActorType: actor.Type, ActorID: &aid, Action: action, ResourceType: "gateway", ResourceID: &rid, Outcome: outcome, RequestID: request, Metadata: meta, CreatedAt: at}
}
func safePrefix(v string) string {
	if len(v) > tokenPrefixLen {
		return v[:tokenPrefixLen]
	}
	return v
}
func publicErr(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrEnrollmentDenied) {
		return &InvalidCredentialError{Cause: ErrEnrollmentDenied}
	}
	if errors.Is(err, store.ErrEnrollmentState) {
		return &StateConflictError{Cause: err}
	}
	return &TransientError{Cause: err}
}
func (s *EnrollmentService) deniedAt(ctx context.Context, started time.Time) error {
	if s.denialDelay != nil {
		s.denialDelay(ctx, started)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return &InvalidCredentialError{Cause: ErrEnrollmentDenied}
}
func (s *EnrollmentService) mapError(ctx context.Context, err error, started time.Time) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var ip *InProgressError
	if errors.As(err, &ip) {
		return ip
	}
	var rl *RateLimitedError
	if errors.As(err, &rl) {
		return rl
	}
	if errors.Is(err, ErrEnrollmentDenied) || errors.Is(err, store.ErrEnrollmentState) {
		return s.deniedAt(ctx, started)
	}
	return &TransientError{Cause: err}
}
