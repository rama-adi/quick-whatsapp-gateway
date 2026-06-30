package store

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// apiKeyColRow mirrors better-auth's ACTUAL apikey columns the gateway reads
// (verified against a live better-auth 1.6.x Drizzle/MySQL schema): snake_case,
// org id in reference_id, TIMESTAMP(3) expires_at/created_at, and permissions as
// the resource->actions map better-auth writes.
func apiKeyColRow() []string {
	return []string{
		"id", "name", "key", "reference_id", "enabled", "expires_at",
		"permissions", "created_at",
	}
}

func TestAPIKeyRepo_GetByHash(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)

	created := time.UnixMilli(100).UTC()
	expires := time.UnixMilli(900).UTC()
	rows := sqlmock.NewRows(apiKeyColRow()).
		AddRow("key_1", "ci", "ba-hash", "org_1", true, expires,
			[]byte(`{"gateway":["read","manage"]}`),
			created)
	mock.ExpectQuery("SELECT .* FROM apikey WHERE .key. = .").
		WithArgs("ba-hash").WillReturnRows(rows)

	got, err := repo.GetByHash(context.Background(), "ba-hash")
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.ID != "key_1" || got.OrganizationID != "org_1" || !got.Enabled {
		t.Fatalf("unexpected key: %+v", got)
	}
	// reference_id is the owning org; better-auth stores no user column on the row.
	if !got.Permissions.Read || got.Permissions.Send || !got.Permissions.Manage {
		t.Fatalf("permissions not scanned: %+v", got.Permissions)
	}
	if got.ExpiresAt == nil || *got.ExpiresAt != 900 {
		t.Fatalf("expiresAt not scanned (epoch-ms from TIMESTAMP): %+v", got.ExpiresAt)
	}
	if got.CreatedAt != 100 {
		t.Fatalf("createdAt not scanned: %d", got.CreatedAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyRepo_GetByHash_NullOrg(t *testing.T) {
	db, mock := newMock(t)
	repo := NewAPIKeyRepo(db)

	rows := sqlmock.NewRows(apiKeyColRow()).
		AddRow("key_2", "ci", "ba-hash2", nil, true, nil,
			[]byte(`{"gateway":["read","send","events"]}`),
			time.UnixMilli(100).UTC())
	mock.ExpectQuery("SELECT .* FROM apikey WHERE .key. = .").
		WithArgs("ba-hash2").WillReturnRows(rows)

	got, err := repo.GetByHash(context.Background(), "ba-hash2")
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.OrganizationID != "" {
		t.Fatalf("expected empty org for null reference_id, got %q", got.OrganizationID)
	}
	if !got.Permissions.Read || !got.Permissions.Send || !got.Permissions.Events || got.Permissions.Manage {
		t.Fatalf("permissions not scanned: %+v", got.Permissions)
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
		AddRow("key_1", "ci", "ba-hash", "org_1", true, nil,
			[]byte(`{"gateway":["read"]}`), time.UnixMilli(100).UTC())
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
	mock.ExpectExec("UPDATE apikey\\s+SET last_request = \\?\\s+WHERE id = \\?").
		WithArgs(time.UnixMilli(123).UTC(), "key_1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.TouchLastRequest(context.Background(), "key_1", 123); err != nil {
		t.Fatalf("TouchLastRequest: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

var _ = domain.APIKey{}
