package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

type EnrollmentTokenRepo struct{ q *storedb.Queries }

func NewEnrollmentTokenRepo(db storedb.DBTX) *EnrollmentTokenRepo {
	return &EnrollmentTokenRepo{storedb.New(db)}
}
func (r *EnrollmentTokenRepo) Issue(ctx context.Context, t domain.EnrollmentToken) error {
	return r.q.IssueGatewayEnrollmentToken(ctx, storedb.IssueGatewayEnrollmentTokenParams{ID: t.ID, GatewayID: t.GatewayID, TokenHash: t.TokenHash, TokenPrefix: t.TokenPrefix, MaxAttempts: t.MaxAttempts, ExpiresAt: t.ExpiresAt, CreatedByUserID: t.CreatedByUserID, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt})
}

// GetForUpdate locks the selector row. Callers must use a transaction and
// compare the presented token digest in constant time before beginning redemption.
func (r *EnrollmentTokenRepo) GetForUpdate(ctx context.Context, id string) (domain.EnrollmentToken, error) {
	t, err := r.q.GetGatewayEnrollmentTokenForUpdate(ctx, storedb.GetGatewayEnrollmentTokenForUpdateParams{ID: id})
	if err != nil {
		return domain.EnrollmentToken{}, err
	}
	return domain.EnrollmentToken{ID: t.ID, GatewayID: t.GatewayID, TokenPrefix: t.TokenPrefix, Status: string(t.Status), TokenHash: t.TokenHash, AttemptCount: t.AttemptCount, MaxAttempts: t.MaxAttempts, ExpiresAt: t.ExpiresAt, CreatedByUserID: t.CreatedByUserID, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, RedemptionNonce: []byte(t.RedemptionNonce.String), CSRSHA256: []byte(t.CsrSha256.String), RedeemingAt: int64PtrFromNull(t.RedeemingAt), LeaseExpiresAt: int64PtrFromNull(t.LeaseExpiresAt), ConsumedAt: int64PtrFromNull(t.ConsumedAt), RevokedAt: int64PtrFromNull(t.RevokedAt)}, nil
}
func (r *EnrollmentTokenRepo) Begin(ctx context.Context, id string, nonce, csrHash []byte, now, leaseUntil int64) (bool, error) {
	n, e := r.q.BeginGatewayEnrollment(ctx, storedb.BeginGatewayEnrollmentParams{RedemptionNonce: binaryNull(nonce), CsrSha256: binaryNull(csrHash), RedeemingAt: sql.NullInt64{Int64: now, Valid: true}, LeaseExpiresAt: sql.NullInt64{Int64: leaseUntil, Valid: true}, UpdatedAt: now, ID: id, ExpiresAt: now, LeaseExpiresAt_2: sql.NullInt64{Int64: now, Valid: true}, CsrSha256_2: binaryNull(csrHash)})
	return n == 1, e
}
func (r *EnrollmentTokenRepo) Finalize(ctx context.Context, id string, nonce, csrHash []byte, now int64) (bool, error) {
	n, e := r.q.FinalizeGatewayEnrollment(ctx, storedb.FinalizeGatewayEnrollmentParams{ConsumedAt: sql.NullInt64{Int64: now, Valid: true}, UpdatedAt: now, ID: id, RedemptionNonce: binaryNull(nonce), CsrSha256: binaryNull(csrHash), LeaseExpiresAt: sql.NullInt64{Int64: now, Valid: true}})
	return n == 1, e
}
func (r *EnrollmentTokenRepo) Release(ctx context.Context, id string, nonce, csrHash []byte, now int64) (bool, error) {
	n, e := r.q.ReleaseGatewayEnrollment(ctx, storedb.ReleaseGatewayEnrollmentParams{UpdatedAt: now, ID: id, RedemptionNonce: binaryNull(nonce), CsrSha256: binaryNull(csrHash), LeaseExpiresAt: sql.NullInt64{Int64: now, Valid: true}})
	return n == 1, e
}

func binaryNull(value []byte) sql.NullString {
	return sql.NullString{String: string(value), Valid: len(value) > 0}
}
func (r *EnrollmentTokenRepo) Revoke(ctx context.Context, id string, now int64) (bool, error) {
	n, e := r.q.RevokeGatewayEnrollmentToken(ctx, storedb.RevokeGatewayEnrollmentTokenParams{RevokedAt: sql.NullInt64{Int64: now, Valid: true}, UpdatedAt: now, ID: id})
	return n == 1, e
}
func (r *EnrollmentTokenRepo) Lock(ctx context.Context, id string, now int64) (bool, error) {
	n, e := r.q.LockGatewayEnrollmentToken(ctx, storedb.LockGatewayEnrollmentTokenParams{UpdatedAt: now, ID: id})
	return n == 1, e
}

type GatewayCertificateRepo struct{ q *storedb.Queries }

func NewGatewayCertificateRepo(db storedb.DBTX) *GatewayCertificateRepo {
	return &GatewayCertificateRepo{storedb.New(db)}
}
func (r *GatewayCertificateRepo) Insert(ctx context.Context, c domain.GatewayCertificate) error {
	return r.q.InsertGatewayCertificate(ctx, storedb.InsertGatewayCertificateParams{ID: c.ID, GatewayID: c.GatewayID, AuthorityID: c.AuthorityID, SerialNumber: c.SerialNumber, CertificatePem: c.CertificatePEM, CertificateFingerprint: c.Fingerprint, NotBefore: c.NotBefore, NotAfter: c.NotAfter, CreatedAt: c.CreatedAt})
}
func (r *GatewayCertificateRepo) Revoke(ctx context.Context, id, reason string, now int64) (bool, error) {
	n, e := r.q.RevokeGatewayCertificate(ctx, storedb.RevokeGatewayCertificateParams{RevokedAt: sql.NullInt64{Int64: now, Valid: true}, RevocationReason: sql.NullString{String: reason, Valid: reason != ""}, ID: id})
	return n == 1, e
}
func (r *GatewayCertificateRepo) List(ctx context.Context, gatewayID string) ([]domain.GatewayCertificate, error) {
	rows, err := r.q.ListGatewayCertificates(ctx, storedb.ListGatewayCertificatesParams{GatewayID: gatewayID})
	if err != nil {
		return nil, err
	}
	out := make([]domain.GatewayCertificate, 0, len(rows))
	for _, c := range rows {
		out = append(out, domain.GatewayCertificate{ID: c.ID, GatewayID: c.GatewayID, AuthorityID: c.AuthorityID, SerialNumber: c.SerialNumber, CertificatePEM: c.CertificatePem, Fingerprint: c.CertificateFingerprint, NotBefore: c.NotBefore, NotAfter: c.NotAfter, CreatedAt: c.CreatedAt, RevokedAt: int64PtrFromNull(c.RevokedAt), RevocationReason: stringPtrFromNull(c.RevocationReason)})
	}
	return out, nil
}

type AuditRepo struct{ q *storedb.Queries }

func NewAuditRepo(db storedb.DBTX) *AuditRepo { return &AuditRepo{storedb.New(db)} }
func (r *AuditRepo) Append(ctx context.Context, e domain.AuditEvent) error {
	if e.ID == "" || e.Action == "" || e.ResourceType == "" || !storedb.AuditEventsActorType(e.ActorType).Valid() || !storedb.AuditEventsOutcome(e.Outcome).Valid() {
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
	return r.q.AppendAuditEvent(ctx, storedb.AppendAuditEventParams{ID: e.ID, OrganizationID: nullString(e.OrganizationID), ActorType: storedb.AuditEventsActorType(e.ActorType), ActorID: nullString(e.ActorID), Action: e.Action, ResourceType: e.ResourceType, ResourceID: nullString(e.ResourceID), Outcome: storedb.AuditEventsOutcome(e.Outcome), RequestID: nullString(e.RequestID), SourceIp: sql.NullString{String: string(e.SourceIP), Valid: len(e.SourceIP) > 0}, Metadata: metadata, CreatedAt: e.CreatedAt})
}
func (r *AuditRepo) ListByResource(ctx context.Context, resourceType, resourceID string, before int64, limit int) ([]domain.AuditEvent, error) {
	rows, err := r.q.ListAuditEventsByResource(ctx, storedb.ListAuditEventsByResourceParams{ResourceType: resourceType, ResourceID: sql.NullString{String: resourceID, Valid: true}, CreatedAt: before, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.AuditEvent, 0, len(rows))
	for _, e := range rows {
		var metadata map[string]any
		_ = json.Unmarshal(e.Metadata, &metadata)
		out = append(out, domain.AuditEvent{ID: e.ID, ActorType: string(e.ActorType), Action: e.Action, ResourceType: e.ResourceType, Outcome: string(e.Outcome), OrganizationID: stringPtrFromNull(e.OrganizationID), ActorID: stringPtrFromNull(e.ActorID), ResourceID: stringPtrFromNull(e.ResourceID), RequestID: stringPtrFromNull(e.RequestID), SourceIP: []byte(e.SourceIp.String), Metadata: metadata, CreatedAt: e.CreatedAt})
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
