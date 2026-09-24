package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func TestOAuthRefreshTokenRepo_RotateRefreshTokenReuseRevokesFamilyInTransaction(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthRefreshTokenRepo(db)
	hash := []byte("old")
	consumed := int64(300)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM oauth_refresh_tokens WHERE token_hash = \\? FOR UPDATE").
		WithArgs(hash).
		WillReturnRows(sqlmock.NewRows([]string{"id", "grant_id", "organization_id", "token_hash", "family_id", "parent_id", "scopes", "issued_at", "expires_at", "consumed_at", "revoked_at"}).
			AddRow("rt_1", "gr_1", "org_1", hash, "fam_1", nil, []byte(`["openid"]`), int64(100), int64(10000), consumed, nil))
	mock.ExpectExec("UPDATE oauth_refresh_tokens SET revoked_at = \\? WHERE family_id = \\? AND revoked_at IS NULL").
		WithArgs(int64(500), "fam_1").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	if _, _, err := repo.RotateRefreshToken(context.Background(), domain.OAuthRefreshRotation{TokenHash: hash, ClientID: "client_1", Now: 500}); err == nil {
		t.Fatal("expected reuse error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthSigningKeyRepo_PromoteNextRollsBackOnPromoteFailure(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthSigningKeyRepo(db)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE oauth_signing_keys SET status = 'retired'").
		WithArgs(int64(300)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE oauth_signing_keys SET status = 'active'").
		WithArgs("missing").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	if err := repo.PromoteNext(context.Background(), "missing", 300); err == nil {
		t.Fatal("expected promote failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
