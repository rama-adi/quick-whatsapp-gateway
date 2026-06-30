package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func backfillImportColRow() []string {
	return []string{
		"id", "session_id", "organization_id", "source", "status", "chats",
		"messages", "identities", "groups_count", "group_members",
		"schema_fingerprint", "error", "created_at", "finished_at",
	}
}

func TestBackfillImportRepo_Insert(t *testing.T) {
	db, mock := newMock(t)
	repo := NewBackfillImportRepo(db)

	b := domain.BackfillImport{
		ID: "bfi_1", SessionID: "sess_1", OrganizationID: "org_1", Source: "crypt15",
		Status: "running", CreatedAt: 100,
	}
	mock.ExpectExec("INSERT INTO backfill_imports").
		WithArgs(b.ID, b.SessionID, b.OrganizationID, b.Source, b.Status, 0, 0, 0, 0, 0,
			(*string)(nil), nil, b.CreatedAt, (*int64)(nil)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.Insert(context.Background(), b); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillImportRepo_Finish(t *testing.T) {
	db, mock := newMock(t)
	repo := NewBackfillImportRepo(db)

	fp := "build=940911514;uv=1;caps=ff92cb72"
	fin := int64(200)
	b := domain.BackfillImport{
		ID: "bfi_1", Status: "succeeded", Chats: 5, Messages: 100, Identities: 10,
		Groups: 2, GroupMembers: 20, SchemaFingerprint: &fp, FinishedAt: &fin,
	}
	mock.ExpectExec("UPDATE backfill_imports\\s+SET status = \\?").
		WithArgs("succeeded", 5, 100, 10, 2, 20, &fp, nil, &fin, "bfi_1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.Finish(context.Background(), b); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillImportRepo_LatestForSession(t *testing.T) {
	db, mock := newMock(t)
	repo := NewBackfillImportRepo(db)

	rows := sqlmock.NewRows(backfillImportColRow()).
		AddRow("bfi_1", "sess_1", "org_1", "crypt15", "succeeded", 5, 100, 10, 2, 20,
			"build=1;caps=ab", nil, int64(100), int64(200))
	mock.ExpectQuery("SELECT .* FROM backfill_imports WHERE session_id = . ORDER BY created_at DESC LIMIT 1").
		WithArgs("sess_1").WillReturnRows(rows)

	got, err := repo.LatestForSession(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("LatestForSession: %v", err)
	}
	if got.ID != "bfi_1" || got.Messages != 100 || got.Status != "succeeded" {
		t.Fatalf("unexpected: %+v", got)
	}
	if got.SchemaFingerprint == nil || *got.SchemaFingerprint != "build=1;caps=ab" {
		t.Fatalf("fingerprint not scanned: %+v", got.SchemaFingerprint)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillImportRepo_LatestForSession_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewBackfillImportRepo(db)
	mock.ExpectQuery("SELECT .* FROM backfill_imports WHERE session_id = .").
		WithArgs("sess_x").WillReturnError(noRows())
	_, err := repo.LatestForSession(context.Background(), "sess_x")
	assertNotFound(t, err)
}

func TestBackfillImportRepo_LastSuccessAt(t *testing.T) {
	db, mock := newMock(t)
	repo := NewBackfillImportRepo(db)

	rows := sqlmock.NewRows([]string{"created_at"}).AddRow(int64(12345))
	mock.ExpectQuery("SELECT created_at FROM backfill_imports WHERE session_id = . AND status = 'succeeded'").
		WithArgs("sess_1").WillReturnRows(rows)

	at, ok, err := repo.LastSuccessAt(context.Background(), "sess_1")
	if err != nil || !ok || at != 12345 {
		t.Fatalf("LastSuccessAt = %d, %v, %v", at, ok, err)
	}

	// No prior success → ok=false, no error.
	mock.ExpectQuery("SELECT created_at FROM backfill_imports WHERE session_id = . AND status = 'succeeded'").
		WithArgs("sess_2").WillReturnError(noRows())
	_, ok, err = repo.LastSuccessAt(context.Background(), "sess_2")
	if err != nil || ok {
		t.Fatalf("expected ok=false, got ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillImportRepo_HasRunningSince(t *testing.T) {
	db, mock := newMock(t)
	repo := NewBackfillImportRepo(db)

	rows := sqlmock.NewRows([]string{"exists"}).AddRow(true)
	mock.ExpectQuery("SELECT EXISTS .*FROM backfill_imports.*status = 'running'.*created_at >= .").
		WithArgs("sess_1", int64(500)).WillReturnRows(rows)
	running, err := repo.HasRunningSince(context.Background(), "sess_1", 500)
	if err != nil || !running {
		t.Fatalf("HasRunningSince = %v, %v", running, err)
	}

	rows = sqlmock.NewRows([]string{"exists"}).AddRow(false)
	mock.ExpectQuery("SELECT EXISTS .*FROM backfill_imports.*status = 'running'.*created_at >= .").
		WithArgs("sess_2", int64(500)).WillReturnRows(rows)
	running, err = repo.HasRunningSince(context.Background(), "sess_2", 500)
	if err != nil || running {
		t.Fatalf("expected not running, got %v, %v", running, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
