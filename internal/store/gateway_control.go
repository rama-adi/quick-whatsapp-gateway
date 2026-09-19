package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

type enrollmentTokenRepo struct{ q *storedb.Queries }

func newEnrollmentTokenRepo(db storedb.DBTX) *enrollmentTokenRepo {
	return &enrollmentTokenRepo{storedb.New(db)}
}
func (r *enrollmentTokenRepo) issue(ctx context.Context, t domain.EnrollmentToken) error {
	return r.q.IssueGatewayEnrollmentToken(ctx, storedb.IssueGatewayEnrollmentTokenParams{
		ID:              t.ID,
		GatewayID:       t.GatewayID,
		TokenHash:       t.TokenHash,
		TokenPrefix:     t.TokenPrefix,
		MaxAttempts:     t.MaxAttempts,
		ExpiresAt:       t.ExpiresAt,
		CreatedByUserID: t.CreatedByUserID,
		CreatedAt:       t.CreatedAt,
		UpdatedAt:       t.UpdatedAt,
	})
}

// GetForUpdate locks the selector row. Callers must use a transaction and
// compare the presented token digest in constant time before beginning redemption.
func (r *enrollmentTokenRepo) getForUpdate(ctx context.Context, id string) (domain.EnrollmentToken, error) {
	t, err := r.q.GetGatewayEnrollmentTokenForUpdate(ctx, storedb.GetGatewayEnrollmentTokenForUpdateParams{ID: id})
	if err != nil {
		return domain.EnrollmentToken{}, err
	}
	return domain.EnrollmentToken{
		ID:              t.ID,
		GatewayID:       t.GatewayID,
		TokenPrefix:     t.TokenPrefix,
		Status:          string(t.Status),
		TokenHash:       t.TokenHash,
		AttemptCount:    t.AttemptCount,
		MaxAttempts:     t.MaxAttempts,
		ExpiresAt:       t.ExpiresAt,
		CreatedByUserID: t.CreatedByUserID,
		CreatedAt:       t.CreatedAt,
		UpdatedAt:       t.UpdatedAt,
		RedemptionNonce: []byte(t.RedemptionNonce.String),
		CSRSHA256:       []byte(t.CsrSha256.String),
		RedeemingAt:     int64PtrFromNull(t.RedeemingAt),
		LeaseExpiresAt:  int64PtrFromNull(t.LeaseExpiresAt),
		ConsumedAt:      int64PtrFromNull(t.ConsumedAt),
		RevokedAt:       int64PtrFromNull(t.RevokedAt),
	}, nil
}
func (r *enrollmentTokenRepo) begin(
	ctx context.Context,
	id string,
	nonce, csrHash []byte,
	now, leaseUntil int64,
) (bool, error) {
	n, e := r.q.BeginGatewayEnrollment(ctx, storedb.BeginGatewayEnrollmentParams{
		RedemptionNonce:  binaryNull(nonce),
		CsrSha256:        binaryNull(csrHash),
		RedeemingAt:      sql.NullInt64{Int64: now, Valid: true},
		LeaseExpiresAt:   sql.NullInt64{Int64: leaseUntil, Valid: true},
		UpdatedAt:        now,
		ID:               id,
		ExpiresAt:        now,
		LeaseExpiresAt_2: sql.NullInt64{Int64: now, Valid: true},
		CsrSha256_2:      binaryNull(csrHash),
	})
	return n == 1, e
}
func (r *enrollmentTokenRepo) finalize(ctx context.Context, id string, nonce, csrHash []byte, now int64) (bool, error) {
	n, e := r.q.FinalizeGatewayEnrollment(ctx, storedb.FinalizeGatewayEnrollmentParams{
		ConsumedAt:      sql.NullInt64{Int64: now, Valid: true},
		UpdatedAt:       now,
		ID:              id,
		RedemptionNonce: binaryNull(nonce),
		CsrSha256:       binaryNull(csrHash),
		LeaseExpiresAt:  sql.NullInt64{Int64: now, Valid: true},
	})
	return n == 1, e
}
func (r *enrollmentTokenRepo) release(ctx context.Context, id string, nonce, csrHash []byte, now int64) (bool, error) {
	n, e := r.q.ReleaseGatewayEnrollment(ctx, storedb.ReleaseGatewayEnrollmentParams{
		UpdatedAt:       now,
		ID:              id,
		RedemptionNonce: binaryNull(nonce),
		CsrSha256:       binaryNull(csrHash),
		LeaseExpiresAt:  sql.NullInt64{Int64: now, Valid: true},
	})
	return n == 1, e
}

func binaryNull(value []byte) sql.NullString {
	return sql.NullString{String: string(value), Valid: len(value) > 0}
}
func (r *enrollmentTokenRepo) lock(ctx context.Context, id string, now int64) (bool, error) {
	n, e := r.q.LockGatewayEnrollmentToken(ctx, storedb.LockGatewayEnrollmentTokenParams{UpdatedAt: now, ID: id})
	return n == 1, e
}
func (r *enrollmentTokenRepo) revokeLiveByGateway(ctx context.Context, gatewayID string, now int64) (int64, error) {
	return r.q.RevokeLiveGatewayEnrollmentTokens(ctx, storedb.RevokeLiveGatewayEnrollmentTokensParams{
		RevokedAt: sql.NullInt64{Int64: now, Valid: true},
		UpdatedAt: now,
		GatewayID: gatewayID,
	})
}

type gatewayCertificateRepo struct{ q *storedb.Queries }

func newGatewayCertificateRepo(db storedb.DBTX) *gatewayCertificateRepo {
	return &gatewayCertificateRepo{storedb.New(db)}
}
func (r *gatewayCertificateRepo) insert(ctx context.Context, c domain.GatewayCertificate) error {
	return r.q.InsertGatewayCertificate(ctx, storedb.InsertGatewayCertificateParams{
		ID:                     c.ID,
		GatewayID:              c.GatewayID,
		AuthorityID:            c.AuthorityID,
		IssuanceKind:           storedb.GatewayCertificatesIssuanceKind(c.IssuanceKind),
		EnrollmentTokenID:      nullString(c.EnrollmentTokenID),
		CsrSha256:              c.CSRSHA256,
		SerialNumber:           c.SerialNumber,
		CertificatePem:         c.CertificatePEM,
		TrustBundlePem:         c.TrustBundlePEM,
		CertificateFingerprint: c.Fingerprint,
		NotBefore:              c.NotBefore,
		NotAfter:               c.NotAfter,
		CreatedAt:              c.CreatedAt,
	})
}
func (r *gatewayCertificateRepo) getByTokenCSR(
	ctx context.Context,
	tokenID string,
	csr []byte,
) (domain.GatewayCertificate, error) {
	c, e := r.q.GetGatewayCertificateByTokenCSR(ctx, storedb.GetGatewayCertificateByTokenCSRParams{
		EnrollmentTokenID: sql.NullString{String: tokenID, Valid: true},
		CsrSha256:         csr,
	})
	if e != nil {
		return domain.GatewayCertificate{}, e
	}
	return certificateFromDB(c), nil
}
func (r *gatewayCertificateRepo) getByGatewayCSR(
	ctx context.Context,
	gatewayID string,
	csr []byte,
) (domain.GatewayCertificate, error) {
	c, err := r.q.GetGatewayCertificateByGatewayCSR(ctx, storedb.GetGatewayCertificateByGatewayCSRParams{
		GatewayID: gatewayID,
		CsrSha256: csr,
	})
	return certificateFromDB(c), err
}
func (r *gatewayCertificateRepo) getForRenewal(
	ctx context.Context,
	id, gatewayID string,
	fingerprint []byte,
	serial string,
) (domain.GatewayCertificate, error) {
	c, err := r.q.GetGatewayCertificateForRenewal(ctx, storedb.GetGatewayCertificateForRenewalParams{
		ID:                     id,
		GatewayID:              gatewayID,
		CertificateFingerprint: fingerprint,
		SerialNumber:           serial,
	})
	return certificateFromDB(c), err
}
func certificateFromDB(c storedb.GatewayCertificate) domain.GatewayCertificate {
	return domain.GatewayCertificate{
		ID:                c.ID,
		GatewayID:         c.GatewayID,
		AuthorityID:       c.AuthorityID,
		IssuanceKind:      string(c.IssuanceKind),
		EnrollmentTokenID: stringPtrFromNull(c.EnrollmentTokenID),
		CSRSHA256:         c.CsrSha256,
		SerialNumber:      c.SerialNumber,
		CertificatePEM:    c.CertificatePem,
		TrustBundlePEM:    c.TrustBundlePem,
		Fingerprint:       c.CertificateFingerprint,
		NotBefore:         c.NotBefore,
		NotAfter:          c.NotAfter,
		RevokedAt:         int64PtrFromNull(c.RevokedAt),
		RevocationReason:  stringPtrFromNull(c.RevocationReason),
		CreatedAt:         c.CreatedAt,
	}
}

type AuditRepo struct{ q *storedb.Queries }

func NewAuditRepo(db storedb.DBTX) *AuditRepo { return &AuditRepo{storedb.New(db)} }
func (r *AuditRepo) Append(ctx context.Context, e domain.AuditEvent) error {
	auditActorValid := storedb.AuditEventsActorType(e.ActorType).Valid()
	auditOutcomeValid := storedb.AuditEventsOutcome(e.Outcome).Valid()
	if e.ID == "" || e.Action == "" || e.ResourceType == "" || !auditActorValid || !auditOutcomeValid {
		return fmt.Errorf("store: invalid audit event")
	}
	if err := validateAuditMetadata(e.Metadata); err != nil {
		return err
	}
	metadata, err := json.Marshal(e.Metadata)
	if err != nil {
		return fmt.Errorf("store: marshal audit metadata: %w", err)
	}
	if len(metadata) > 16*1024 {
		return fmt.Errorf("store: audit metadata exceeds 16384 bytes")
	}
	return r.q.AppendAuditEvent(ctx, storedb.AppendAuditEventParams{
		ID:             e.ID,
		OrganizationID: nullString(e.OrganizationID),
		ActorType:      storedb.AuditEventsActorType(e.ActorType),
		ActorID:        nullString(e.ActorID),
		Action:         e.Action,
		ResourceType:   e.ResourceType,
		ResourceID:     nullString(e.ResourceID),
		Outcome:        storedb.AuditEventsOutcome(e.Outcome),
		RequestID:      nullString(e.RequestID),
		SourceIp:       sql.NullString{String: string(e.SourceIP), Valid: len(e.SourceIP) > 0},
		Metadata:       metadata,
		CreatedAt:      e.CreatedAt,
	})
}
func (r *AuditRepo) ListByResource(
	ctx context.Context,
	resourceType, resourceID string,
	before int64,
	limit int,
) ([]domain.AuditEvent, error) {
	rows, err := r.q.ListAuditEventsByResource(ctx, storedb.ListAuditEventsByResourceParams{
		ResourceType: resourceType,
		ResourceID:   sql.NullString{String: resourceID, Valid: true},
		CreatedAt:    before,
		Limit:        int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.AuditEvent, 0, len(rows))
	for _, e := range rows {
		metadata := map[string]any{}
		_ = json.Unmarshal(e.Metadata, &metadata)
		out = append(out, domain.AuditEvent{
			ID:             e.ID,
			ActorType:      string(e.ActorType),
			Action:         e.Action,
			ResourceType:   e.ResourceType,
			Outcome:        string(e.Outcome),
			OrganizationID: stringPtrFromNull(e.OrganizationID),
			ActorID:        stringPtrFromNull(e.ActorID),
			ResourceID:     stringPtrFromNull(e.ResourceID),
			RequestID:      stringPtrFromNull(e.RequestID),
			SourceIP:       []byte(e.SourceIp.String),
			Metadata:       metadata,
			CreatedAt:      e.CreatedAt,
		})
	}
	return out, nil
}

func validateAuditMetadata(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			normalized := strings.Map(func(r rune) rune {
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					return unicode.ToLower(r)
				}
				return -1
			}, key)
			allowed := strings.HasSuffix(normalized, "id") || strings.HasSuffix(normalized, "fingerprint")
			for _, bad := range []string{"token", "secret", "privatekey", "authorization", "csr", "certificate", "pem"} {
				if strings.Contains(normalized, bad) && !allowed {
					return fmt.Errorf("store: sensitive audit metadata key %q", key)
				}
			}
			if err := validateAuditMetadata(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := validateAuditMetadata(child); err != nil {
				return err
			}
		}
	}
	return nil
}
