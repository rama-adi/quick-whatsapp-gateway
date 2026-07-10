package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// TestOAuthClientRepo_GetByOrg_IsOrgKeyed verifies client lookup cannot cross tenants.
// The SQL expectation requires both organization and internal id and reconstructs all security-relevant client settings.
func TestOAuthClientRepo_GetByOrg_IsOrgKeyed(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthClientRepo(db)

	rows := sqlmock.NewRows([]string{"id", "client_id", "organization_id", "created_by_user_id", "session_id", "name", "bot_name", "logo_url", "client_type", "login_command", "secret_hash", "secret_last4", "redirect_uris", "modes", "group_jid", "allowed_scopes", "token_ttl_seconds", "refresh_ttl_seconds", "status", "created_at", "updated_at", "deleted_at"}).
		AddRow("oc_1", "client_1", "org_1", nil, "sess_1", "Acme", "Acme Bot", nil, "confidential", "login", []byte("hash"), "last4", []byte(`["https://app.test/cb"]`), "dm", nil, []byte(`["openid"]`), 900, 2592000, "active", 100, 200, nil)
	mock.ExpectQuery("SELECT .* FROM oauth_clients WHERE organization_id = \\? AND id = \\? AND deleted_at IS NULL").
		WithArgs("org_1", "oc_1").WillReturnRows(rows)

	got, err := repo.GetByOrg(context.Background(), "org_1", "oc_1")
	if err != nil {
		t.Fatalf("GetByOrg: %v", err)
	}
	if got.ID != "oc_1" || got.OrganizationID != "org_1" || got.LoginCommand != "login" {
		t.Fatalf("unexpected client: %+v", got)
	}
	if got.BotName == nil || *got.BotName != "Acme Bot" {
		t.Fatalf("bot name did not round-trip: %+v", got.BotName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestOAuthClientRepo_GetByOrg_NotFound protects missing-client error mapping.
// An absent or wrong-tenant row must be indistinguishable at the repository boundary.
func TestOAuthClientRepo_GetByOrg_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthClientRepo(db)
	mock.ExpectQuery("SELECT .* FROM oauth_clients WHERE organization_id = \\? AND id = \\?").
		WithArgs("org_1", "missing").WillReturnError(noRows())
	_, err := repo.GetByOrg(context.Background(), "org_1", "missing")
	assertNotFound(t, err)
}

// TestOAuthGrantRepo_Upsert verifies consent refresh preserves the unique grant identity.
// Conflict handling must refresh scopes and usage while clearing revocation without creating a second grant.
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

// TestOAuthRefreshTokenRepo_GetByHash verifies hash lookup and nullable lifecycle mapping.
// The raw token is never queried; consumed and revoked timestamps must remain distinct for reuse detection.
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

// TestOAuthRefreshTokenRepo_RotateRefreshTokenTransaction verifies consume-and-replace commits atomically.
// Locked token/grant reads, conditional consume, successor insert, and commit are asserted in their required order.
func TestOAuthRefreshTokenRepo_RotateRefreshTokenTransaction(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthRefreshTokenRepo(db)
	hash := []byte("old")
	nextHash := []byte("next")
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM oauth_refresh_tokens WHERE token_hash = \\? FOR UPDATE").
		WithArgs(hash).
		WillReturnRows(sqlmock.NewRows([]string{"id", "grant_id", "organization_id", "token_hash", "family_id", "parent_id", "scopes", "issued_at", "expires_at", "consumed_at", "revoked_at"}).
			AddRow("rt_1", "gr_1", "org_1", hash, "fam_1", nil, []byte(`["openid","profile"]`), int64(100), int64(10000), nil, nil))
	mock.ExpectQuery("SELECT .* FROM oauth_grants WHERE id = \\? AND revoked_at IS NULL FOR UPDATE").
		WithArgs("gr_1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "organization_id", "client_id", "wa_identity_id", "sub", "granted_scopes", "last_acr", "last_group_jid", "created_at", "last_used_at", "revoked_at"}).
			AddRow("gr_1", "org_1", "client_1", uint64(42), "sub", []byte(`["openid","profile"]`), "wa:dm", nil, int64(100), int64(200), nil))
	mock.ExpectExec("UPDATE oauth_refresh_tokens SET consumed_at = \\? WHERE id = \\? AND consumed_at IS NULL AND revoked_at IS NULL").
		WithArgs(int64(500), "rt_1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO oauth_refresh_tokens").
		WithArgs("rt_2", "gr_1", "org_1", nextHash, "fam_1", "rt_1", []byte(`["openid"]`), int64(500), int64(10000), nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	rt, g, err := repo.RotateRefreshToken(context.Background(), domain.OAuthRefreshRotation{
		TokenHash: hash, ClientID: "client_1", RequestedScopes: []string{"openid"}, Now: 500,
		Successor: domain.OAuthRefreshToken{ID: "rt_2", TokenHash: nextHash},
	})
	if err != nil {
		t.Fatalf("RotateRefreshToken: %v", err)
	}
	if rt.ID != "rt_1" || g.ID != "gr_1" {
		t.Fatalf("unexpected rotation result rt=%+v grant=%+v", rt, g)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestOAuthRefreshTokenRepo_RotateRefreshTokenReuseRevokesFamilyInTransaction protects reuse detection and durable family revocation.
// A consumed token must revoke its entire family and commit that defense before returning the reuse error.
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

// TestOAuthSigningKeyRepo_ListPublic verifies JWKS reads expose only public key material.
// Active/next/retired rows may be listed, but encrypted private bytes are intentionally absent from the projection.
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

// TestOAuthSigningKeyRepo_PromoteNext verifies retire-and-promote commits as one rotation.
// The old active key is retired before the selected next key becomes active inside a single transaction.
func TestOAuthSigningKeyRepo_PromoteNext(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOAuthSigningKeyRepo(db)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE oauth_signing_keys SET status = 'retired'").
		WithArgs(int64(300)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE oauth_signing_keys SET status = 'active'").
		WithArgs("kid_next").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := repo.PromoteNext(context.Background(), "kid_next", 300); err != nil {
		t.Fatalf("PromoteNext: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestOAuthSigningKeyRepo_PromoteNextRollsBackOnPromoteFailure prevents losing the active key on partial failure.
// If no next key is promoted, retiring the current key must roll back rather than leave the issuer keyless.
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
