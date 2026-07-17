package store

import (
	"context"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

type PKIAuthorityRepo struct{ q *storedb.Queries }

func NewPKIAuthorityRepo(db storedb.DBTX) *PKIAuthorityRepo {
	return &PKIAuthorityRepo{storedb.New(db)}
}
func (r *PKIAuthorityRepo) Insert(ctx context.Context, a domain.PKIAuthority) error {
	return r.q.InsertPKIAuthority(ctx, storedb.InsertPKIAuthorityParams{ID: a.ID, CertificatePem: a.CertificatePEM, CertificateFingerprint: a.CertificateFingerprint, EncryptedPrivateKey: a.EncryptedPrivateKey, PrivateKeyNonce: a.PrivateKeyNonce, EncryptionKeyID: a.EncryptionKeyID, NotBefore: a.NotBefore, NotAfter: a.NotAfter, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt})
}
func (r *PKIAuthorityRepo) ActiveForUpdate(ctx context.Context, now int64) (domain.PKIAuthority, error) {
	a, err := r.q.GetActivePKIAuthorityForUpdate(ctx, storedb.GetActivePKIAuthorityForUpdateParams{NotAfter: now})
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	return mapPKIAuthority(a), nil
}
func (r *PKIAuthorityRepo) activeForRotation(ctx context.Context) (domain.PKIAuthority, error) {
	a, err := r.q.GetActivePKIAuthorityForRotation(ctx)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	return mapPKIAuthority(a), nil
}
func mapPKIAuthority(a storedb.PkiAuthority) domain.PKIAuthority {
	return domain.PKIAuthority{ID: a.ID, Status: string(a.Status), CertificatePEM: a.CertificatePem, EncryptionKeyID: a.EncryptionKeyID, CertificateFingerprint: a.CertificateFingerprint, EncryptedPrivateKey: a.EncryptedPrivateKey, PrivateKeyNonce: a.PrivateKeyNonce, NotBefore: a.NotBefore, NotAfter: a.NotAfter, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
}
func (r *PKIAuthorityRepo) lockRotation(ctx context.Context) error {
	_, err := r.q.LockPKIRotation(ctx)
	return err
}
func (r *PKIAuthorityRepo) retireActive(ctx context.Context, now int64) (int64, error) {
	return r.q.RetireActivePKIAuthorities(ctx, storedb.RetireActivePKIAuthoritiesParams{UpdatedAt: now})
}
func (r *PKIAuthorityRepo) MarkRetired(ctx context.Context, id string, now int64) (bool, error) {
	n, e := r.q.MarkPKIAuthorityRetired(ctx, storedb.MarkPKIAuthorityRetiredParams{UpdatedAt: now, ID: id})
	return n == 1, e
}
