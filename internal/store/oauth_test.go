package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestOAuthClientRepo_GetByOrg_IsOrgKeyed(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthClientRepo(db)

	rows := sqlmock.NewRows([]string{"id", "client_id", "organization_id", "created_by_user_id", "session_id", "name", "logo_url", "client_type", "login_command", "secret_hash", "secret_last4", "redirect_uris", "modes", "group_jid", "allowed_scopes", "token_ttl_seconds", "refresh_ttl_seconds", "status", "created_at", "updated_at", "deleted_at"}).
		AddRow("oc_1", "client_1", "org_1", nil, "sess_1", "Acme", nil, "confidential", "login", []byte("hash"), "last4", []byte(`["https://app.test/cb"]`), "dm", nil, []byte(`["openid"]`), 900, 2592000, "active", 100, 200, nil)
	mock.ExpectQuery("SELECT .* FROM oauth_clients WHERE organization_id = \\? AND id = \\? AND deleted_at IS NULL").
		WithArgs("org_1", "oc_1").WillReturnRows(rows)

	got, err := repo.GetByOrg(context.Background(), "org_1", "oc_1")
	if err != nil {
		t.Fatalf("GetByOrg: %v", err)
	}
	if got.ID != "oc_1" || got.OrganizationID != "org_1" || got.LoginCommand != "login" {
		t.Fatalf("unexpected client: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthClientRepo_GetByOrg_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthClientRepo(db)
	mock.ExpectQuery("SELECT .* FROM oauth_clients WHERE organization_id = \\? AND id = \\?").
		WithArgs("org_1", "missing").WillReturnError(noRows())
	_, err := repo.GetByOrg(context.Background(), "org_1", "missing")
	assertNotFound(t, err)
}

func TestOAuthGrantRepo_Upsert(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthGrantRepo(db)
	mock.ExpectExec("INSERT INTO oauth_grants .* ON DUPLICATE KEY UPDATE").
		WithArgs("gr_1", "org_1", "client_1", uint64(42), "sub", []byte(`["openid"]`), "wa:dm", nil, int64(100), int64(100), nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.Upsert(context.Background(), domain.OAuthGrant{
		ID: "gr_1", OrganizationID: "org_1", ClientID: "client_1", WAIdentityID: 42,
		Sub: "sub", GrantedScopes: []byte(`["openid"]`), LastACR: "wa:dm", CreatedAt: 100, LastUsedAt: 100,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthRefreshTokenRepo_GetByHash(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthRefreshTokenRepo(db)
	hash := []byte("sha")
	rows := sqlmock.NewRows([]string{"id", "grant_id", "organization_id", "token_hash", "family_id", "parent_id", "scopes", "issued_at", "expires_at", "consumed_at", "revoked_at"}).
		AddRow("rt_1", "gr_1", "org_1", hash, "fam_1", nil, []byte(`["openid"]`), 100, 200, nil, nil)
	mock.ExpectQuery("SELECT .* FROM oauth_refresh_tokens WHERE token_hash = \\?").
		WithArgs(hash).WillReturnRows(rows)
	got, err := repo.GetByHash(context.Background(), hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.ID != "rt_1" || got.FamilyID != "fam_1" {
		t.Fatalf("unexpected refresh token: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthSigningKeyRepo_ListPublic(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthSigningKeyRepo(db)
	rows := sqlmock.NewRows([]string{"kid", "alg", "public_jwk", "private_enc", "status", "created_at", "retired_at"}).
		AddRow("kid_active", "EdDSA", []byte(`{"kty":"OKP"}`), []byte("enc"), "active", 100, nil).
		AddRow("kid_next", "EdDSA", []byte(`{"kty":"OKP"}`), []byte("enc"), "next", 200, nil)
	mock.ExpectQuery("SELECT .* FROM oauth_signing_keys WHERE status IN").
		WillReturnRows(rows)
	got, err := repo.ListPublic(context.Background())
	if err != nil {
		t.Fatalf("ListPublic: %v", err)
	}
	if len(got) != 2 || got[0].Status != "active" || got[1].Status != "next" {
		t.Fatalf("unexpected keys: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthSigningKeyRepo_PromoteNext(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthSigningKeyRepo(db)
	mock.ExpectExec("UPDATE oauth_signing_keys SET status = 'retired'").
		WithArgs(int64(300)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE oauth_signing_keys SET status = 'active'").
		WithArgs("kid_next").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.PromoteNext(context.Background(), "kid_next", 300); err != nil {
		t.Fatalf("PromoteNext: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
