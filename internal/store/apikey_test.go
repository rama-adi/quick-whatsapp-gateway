package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func apiKeyColRow() []string {
	return []string{
		"id", "name", "key", "userId", "organizationId", "enabled", "expiresAt",
		"permissions", "createdAt",
	}
}

func TestAPIKeyRepo_GetByHash(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)

	rows := sqlmock.NewRows(apiKeyColRow()).
		AddRow("key_1", "ci", "ba-hash", "user_1", "org_1", true, int64(900),
			[]byte(`{"read":true,"send":false,"manage":true,"events":false}`),
			int64(100))
	mock.ExpectQuery("SELECT .* FROM apikey WHERE .key. = .").
		WithArgs("ba-hash").WillReturnRows(rows)

	got, err := repo.GetByHash(context.Background(), "ba-hash")
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.ID != "key_1" || got.OrganizationID != "org_1" || !got.Enabled {
		t.Fatalf("unexpected key: %+v", got)
	}
	if got.UserID == nil || *got.UserID != "user_1" {
		t.Fatalf("userId not scanned: %+v", got.UserID)
	}
	if !got.Permissions.Read || got.Permissions.Send || !got.Permissions.Manage {
		t.Fatalf("permissions not scanned: %+v", got.Permissions)
	}
	if got.ExpiresAt == nil || *got.ExpiresAt != 900 {
		t.Fatalf("expiresAt not scanned")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyRepo_GetByHash_NullOrg(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)

	rows := sqlmock.NewRows(apiKeyColRow()).
		AddRow("key_2", "ci", "ba-hash2", "user_2", nil, true, nil,
			[]byte(`{"read":true,"send":true,"manage":false,"events":true}`),
			int64(100))
	mock.ExpectQuery("SELECT .* FROM apikey WHERE .key. = .").
		WithArgs("ba-hash2").WillReturnRows(rows)

	got, err := repo.GetByHash(context.Background(), "ba-hash2")
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.OrganizationID != "" {
		t.Fatalf("expected empty org for null organizationId, got %q", got.OrganizationID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyRepo_GetByHash_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)
	mock.ExpectQuery("SELECT .* FROM apikey WHERE .key. = .").
		WithArgs("nope").WillReturnError(noRows())
	_, err := repo.GetByHash(context.Background(), "nope")
	assertNotFound(t, err)
}

func TestAPIKeyRepo_GetByID(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)
	rows := sqlmock.NewRows(apiKeyColRow()).
		AddRow("key_1", "ci", "ba-hash", nil, "org_1", true, nil,
			[]byte(`{"read":true}`), int64(100))
	mock.ExpectQuery("SELECT .* FROM apikey WHERE id = .").
		WithArgs("key_1").WillReturnRows(rows)
	got, err := repo.GetByID(context.Background(), "key_1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != "key_1" {
		t.Fatalf("unexpected key: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyRepo_TouchLastRequest(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)
	mock.ExpectExec("UPDATE apikey SET lastRequest=. WHERE id=.").
		WithArgs(int64(123), "key_1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.TouchLastRequest(context.Background(), "key_1", 123); err != nil {
		t.Fatalf("TouchLastRequest: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

var _ = domain.APIKey{}
