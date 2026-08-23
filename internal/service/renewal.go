package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// RenewalCredential is the incumbent identity already authenticated by the
// private transport. Callers must not derive any of these fields from an RPC
// payload.
type RenewalCredential struct {
	GatewayID, CertificateID, SerialNumber string
	Fingerprint                            []byte
}

type RenewalInput struct {
	Credential RenewalCredential
	CSRDER     []byte
}

type RenewalPersistence interface {
	PrepareRenewal(context.Context, store.PrepareRenewalInput) (*domain.GatewayCertificate, error)
	FinalizeRenewal(context.Context, store.FinalizeRenewalInput) (domain.GatewayCertificate, error)
}

type RenewalDependencies struct {
	Store RenewalPersistence
	Clock func() time.Time
	IDs   IDGenerator
}

// RenewalService implements the transport-independent authenticated renewal
// transaction. Signing deliberately happens between the short prepare and
// finalize transactions; FinalizeRenewal revalidates the incumbent under lock.
type RenewalService struct {
	store  RenewalPersistence
	signer pki.CertificateSigner
	now    func() time.Time
	id     IDGenerator
}

func NewRenewalService(db *sql.DB, signer pki.CertificateSigner) (*RenewalService, error) {
	return NewRenewalServiceWithDependencies(signer, RenewalDependencies{
		Store: store.NewEnrollmentStore(db),
		Clock: time.Now,
		IDs:   func() string { return ulid.Make().String() },
	})
}

func NewRenewalServiceWithDependencies(
	signer pki.CertificateSigner,
	deps RenewalDependencies,
) (*RenewalService, error) {
	if signer == nil || deps.Store == nil || deps.Clock == nil || deps.IDs == nil {
		return nil, errors.New("renewal dependencies required")
	}
	return &RenewalService{store: deps.Store, signer: signer, now: deps.Clock, id: deps.IDs}, nil
}

func (s *RenewalService) Renew(ctx context.Context, input RenewalInput) (EnrollmentResult, error) {
	credential := input.Credential
	credentialIncomplete := credential.GatewayID == "" || credential.CertificateID == "" ||
		credential.SerialNumber == "" || len(credential.Fingerprint) == 0
	if credentialIncomplete {
		return EnrollmentResult{}, &InvalidCredentialError{Cause: ErrEnrollmentDenied}
	}
	csr, err := pki.ValidateCSR(input.CSRDER, credential.GatewayID)
	if err != nil {
		return EnrollmentResult{}, &InvalidCredentialError{Cause: ErrEnrollmentDenied}
	}
	hash := csr.DERHash()
	now := s.clock()
	storeCredential := store.RenewalCredential{
		GatewayID:     credential.GatewayID,
		CertificateID: credential.CertificateID,
		SerialNumber:  credential.SerialNumber,
		Fingerprint:   append([]byte(nil), credential.Fingerprint...),
	}
	replay, err := s.store.PrepareRenewal(ctx, store.PrepareRenewalInput{
		Credential: storeCredential,
		CSRHash:    hash[:],
		Now:        now.UnixMilli(),
	})
	if err != nil {
		return EnrollmentResult{}, s.mapRenewalError(ctx, err)
	}
	if replay != nil {
		return resultFromCertificate(*replay), nil
	}

	signed, err := s.signer.Sign(ctx, pki.SignRequest{
		GatewayID: credential.GatewayID,
		CSR:       csr,
	})
	if err != nil {
		if ctx.Err() != nil {
			return EnrollmentResult{}, ctx.Err()
		}
		return EnrollmentResult{}, &TransientError{Cause: err}
	}
	if err = pki.ValidateSignedGateway(signed, csr, credential.GatewayID, s.clock()); err != nil {
		return EnrollmentResult{}, &TransientError{Cause: err}
	}
	issuedAt := s.clock().UnixMilli()
	certificate := domain.GatewayCertificate{
		ID:             s.id(),
		GatewayID:      credential.GatewayID,
		AuthorityID:    signed.AuthorityID,
		IssuanceKind:   "renewal",
		CSRSHA256:      hash[:],
		SerialNumber:   signed.Serial.String(),
		CertificatePEM: string(signed.ChainPEM),
		TrustBundlePEM: string(signed.TrustBundlePEM),
		Fingerprint:    signed.Fingerprint,
		NotBefore:      signed.NotBefore.UnixMilli(),
		NotAfter:       signed.NotAfter.UnixMilli(),
		CreatedAt:      issuedAt,
	}
	persisted, err := s.store.FinalizeRenewal(ctx, store.FinalizeRenewalInput{
		Credential:     storeCredential,
		CSRHash:        hash[:],
		Now:            issuedAt,
		Certificate:    certificate,
		SucceededAudit: renewalAudit(s.id(), credential.GatewayID, signed.Fingerprint, issuedAt),
	})
	if err == nil {
		return resultFromCertificate(persisted), nil
	}
	// A completed transaction may be reported ambiguously. Re-reading through
	// PrepareRenewal is safe and returns only the exact persisted replay.
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	replayed, recoveryErr := s.store.PrepareRenewal(recoveryCtx, store.PrepareRenewalInput{
		Credential: storeCredential,
		CSRHash:    hash[:],
		Now:        s.clock().UnixMilli(),
	})
	if recoveryErr == nil && replayed != nil {
		return resultFromCertificate(*replayed), nil
	}
	return EnrollmentResult{}, s.mapRenewalError(ctx, err)
}

func (s *RenewalService) clock() time.Time { return s.now().UTC() }

func (s *RenewalService) mapRenewalError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, store.ErrRenewalState) {
		return &InvalidCredentialError{Cause: ErrEnrollmentDenied}
	}
	return &TransientError{Cause: err}
}

func renewalAudit(id, gatewayID string, fingerprint []byte, now int64) domain.AuditEvent {
	resourceID, actorID := gatewayID, gatewayID
	return domain.AuditEvent{
		ID:           id,
		ActorType:    actorTypeNode,
		ActorID:      &actorID,
		Action:       "renewal.succeeded",
		ResourceType: "gateway",
		ResourceID:   &resourceID,
		Outcome:      "success",
		Metadata:     map[string]any{"certificate_fingerprint": fmt.Sprintf("%x", fingerprint)},
		CreatedAt:    now,
	}
}
