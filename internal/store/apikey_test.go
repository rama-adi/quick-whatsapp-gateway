package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func apiKeyColRow() []string {
	return []string{
		"id", "tenant_id", "name", "key_prefix", "key_hash", "scope", "permissions",
		"last_used_at", "expires_at", "revoked_at", "created_at",
	}
}

func TestAPIKeyRepo_Create(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)

	k := domain.APIKey{
		ID: "wak_1", TenantID: "ten_1", Name: "ci", KeyPrefix: "wak_ab12",
		KeyHash: "argonhash", Scope: domain.ScopeTenant,
		Permissions: domain.Permissions{Read: true, Send: true}, CreatedAt: 100,
	}
	perms, _ := json.Marshal(k.Permissions)
	mock.ExpectExec("INSERT INTO api_keys").
		WithArgs(k.ID, k.TenantID, k.Name, k.KeyPrefix, k.KeyHash, k.Scope, perms,
			k.LastUsedAt, k.ExpiresAt, k.RevokedAt, k.CreatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.Create(context.Background(), k); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyRepo_GetByPrefix(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)

	rows := sqlmock.NewRows(apiKeyColRow()).
		AddRow("wak_1", "ten_1", "ci", "wak_ab12", "argonhash", "tenant",
			[]byte(`{"read":true,"send":false,"manage":true,"events":false}`),
			int64(900), nil, nil, int64(100))
	mock.ExpectQuery("SELECT .* FROM api_keys WHERE key_prefix = .").
		WithArgs("wak_ab12").WillReturnRows(rows)

	got, err := repo.GetByPrefix(context.Background(), "wak_ab12")
	if err != nil {
		t.Fatalf("GetByPrefix: %v", err)
	}
	if got.ID != "wak_1" || got.Scope != domain.ScopeTenant {
		t.Fatalf("unexpected key: %+v", got)
	}
	if !got.Permissions.Read || got.Permissions.Send || !got.Permissions.Manage {
		t.Fatalf("permissions not scanned: %+v", got.Permissions)
	}
	if got.LastUsedAt == nil || *got.LastUsedAt != 900 {
		t.Fatalf("last_used_at not scanned")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyRepo_GetByPrefix_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)
	mock.ExpectQuery("SELECT .* FROM api_keys WHERE key_prefix = .").
		WithArgs("nope").WillReturnError(noRows())
	_, err := repo.GetByPrefix(context.Background(), "nope")
	assertNotFound(t, err)
}

func TestAPIKeyRepo_UpdateLastUsed(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)
	mock.ExpectExec("UPDATE api_keys SET last_used_at=. WHERE id=.").
		WithArgs(int64(123), "wak_1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.UpdateLastUsed(context.Background(), "wak_1", 123); err != nil {
		t.Fatalf("UpdateLastUsed: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyRepo_Revoke(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)
	mock.ExpectExec("UPDATE api_keys SET revoked_at=. WHERE id=.").
		WithArgs(int64(500), "wak_1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.Revoke(context.Background(), "wak_1", 500); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyRepo_Rotate(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)
	mock.ExpectExec("UPDATE api_keys SET key_prefix=., key_hash=., revoked_at=NULL, last_used_at=NULL WHERE id=.").
		WithArgs("wak_new", "newhash", "wak_1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.Rotate(context.Background(), "wak_1", "wak_new", "newhash"); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyRepo_Rotate_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)
	mock.ExpectExec("UPDATE api_keys SET key_prefix=").
		WithArgs("wak_new", "newhash", "missing").WillReturnResult(sqlmock.NewResult(0, 0))
	err := repo.Rotate(context.Background(), "missing", "wak_new", "newhash")
	assertNotFound(t, err)
}
