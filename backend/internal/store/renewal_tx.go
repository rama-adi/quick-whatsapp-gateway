package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

var ErrRenewalState = errors.New("certificate renewal state mismatch")

type RenewalCredential struct {
	GatewayID, CertificateID, SerialNumber string
	Fingerprint                            []byte
}

type PrepareRenewalInput struct {
	Credential RenewalCredential
	CSRHash    []byte
	Now        int64
}

type FinalizeRenewalInput struct {
	Credential     RenewalCredential
	CSRHash        []byte
	Now            int64
	Certificate    domain.GatewayCertificate
	SucceededAudit domain.AuditEvent
}

// PrepareRenewal revalidates the authenticated incumbent and returns exact
// persisted issuance material when this CSR has already succeeded.
func (s *EnrollmentStore) PrepareRenewal(ctx context.Context, in PrepareRenewalInput) (*domain.GatewayCertificate, error) {
	var replay *domain.GatewayCertificate
	err := s.withGatewayTx(ctx, in.Credential.GatewayID, func(t *enrollmentTx) error {
		if err := validateRenewalCredential(ctx, t, in.Credential, in.Now); err != nil {
			return err
		}
		cert, err := newGatewayCertificateRepo(t.tx).getByGatewayCSR(ctx, in.Credential.GatewayID, in.CSRHash)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || !renewalReplayMatches(cert, in.Credential.GatewayID, in.CSRHash, in.Now) {
			return ErrRenewalState
		}
		replay = &cert
		return nil
	})
	return replay, normalizeRenewalError(err)
}

// FinalizeRenewal re-locks the gateway and incumbent certificate after signing,
// closing the authorization/signing TOCTOU window. A concurrent identical CSR
// is recovered as an exact replay instead of surfacing a uniqueness race.
func (s *EnrollmentStore) FinalizeRenewal(ctx context.Context, in FinalizeRenewalInput) (domain.GatewayCertificate, error) {
	out := in.Certificate
	err := s.withGatewayTx(ctx, in.Credential.GatewayID, func(t *enrollmentTx) error {
		if err := validateRenewalCredential(ctx, t, in.Credential, in.Now); err != nil {
			return err
		}
		repo := newGatewayCertificateRepo(t.tx)
		persisted, err := repo.getByGatewayCSR(ctx, in.Credential.GatewayID, in.CSRHash)
		if err == nil {
			if !renewalReplayMatches(persisted, in.Credential.GatewayID, in.CSRHash, in.Now) {
				return ErrRenewalState
			}
			out = persisted
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) || !newRenewalMatches(in.Certificate, in.Credential.GatewayID, in.CSRHash, in.Now) {
			return ErrRenewalState
		}
		if err = repo.insert(ctx, in.Certificate); err != nil {
			persisted, lookupErr := repo.getByGatewayCSR(ctx, in.Credential.GatewayID, in.CSRHash)
			if lookupErr != nil || !renewalReplayMatches(persisted, in.Credential.GatewayID, in.CSRHash, in.Now) {
				return err
			}
			out = persisted
			return nil
		}
		return NewAuditRepo(t.tx).Append(ctx, in.SucceededAudit)
	})
	return out, normalizeRenewalError(err)
}

func validateRenewalCredential(ctx context.Context, t *enrollmentTx, credential RenewalCredential, now int64) error {
	if !t.gatewayRecoveryEligible() {
		return ErrRenewalState
	}
	cert, err := newGatewayCertificateRepo(t.tx).getForRenewal(ctx, credential.CertificateID, credential.GatewayID, credential.Fingerprint, credential.SerialNumber)
	if err != nil || cert.RevokedAt != nil || cert.NotBefore > now || cert.NotAfter <= now {
		return ErrRenewalState
	}
	return nil
}

func newRenewalMatches(cert domain.GatewayCertificate, gatewayID string, csr []byte, now int64) bool {
	return cert.GatewayID == gatewayID && cert.IssuanceKind == "renewal" && cert.EnrollmentTokenID == nil &&
		equalBytes(cert.CSRSHA256, csr) && cert.NotBefore <= now && cert.NotAfter > now
}

func renewalReplayMatches(cert domain.GatewayCertificate, gatewayID string, csr []byte, now int64) bool {
	return newRenewalMatches(cert, gatewayID, csr, now) && cert.RevokedAt == nil
}

func normalizeRenewalError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRenewalState
	}
	return err
}
