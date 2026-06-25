package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func sessionColRow() []string {
	return []string{
		"id", "tenant_id", "label", "status", "wa_jid", "wa_lid", "phone_number",
		"is_admin_session", "auto_read", "presence_typing", "rate_per_min", "rate_per_hour",
		"last_connected_at", "created_at", "updated_at",
	}
}

func TestSessionRepo_Create(t *testing.T) {
	db, mock := newMock(t)
	repo := NewSessionRepo(db)

	s := domain.WASession{
		ID: "sess_1", TenantID: "ten_1", Label: strptr("phone"), Status: domain.SessionStopped,
		AutoRead: true, RatePerMin: 20, RatePerHour: 200, CreatedAt: 100, UpdatedAt: 100,
	}
	mock.ExpectExec("INSERT INTO wa_sessions").
		WithArgs(s.ID, s.TenantID, s.Label, s.Status, s.WAJID, s.WALID, s.PhoneNumber, s.IsAdminSession,
			s.AutoRead, s.PresenceTyping, s.RatePerMin, s.RatePerHour, s.LastConnectedAt, s.CreatedAt, s.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepo_Get(t *testing.T) {
	db, mock := newMock(t)
	repo := NewSessionRepo(db)

	rows := sqlmock.NewRows(sessionColRow()).
		AddRow("sess_1", "ten_1", "phone", "working", "628@s.whatsapp.net", nil, "628",
			false, true, false, 20, 200, int64(500), int64(100), int64(200))
	mock.ExpectQuery("SELECT .* FROM wa_sessions WHERE id = .").
		WithArgs("sess_1").WillReturnRows(rows)

	got, err := repo.Get(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "sess_1" || got.Status != domain.SessionWorking {
		t.Fatalf("unexpected session: %+v", got)
	}
	if got.WAJID == nil || *got.WAJID != "628@s.whatsapp.net" {
		t.Fatalf("wa_jid not scanned: %+v", got.WAJID)
	}
	if got.LastConnectedAt == nil || *got.LastConnectedAt != 500 {
		t.Fatalf("last_connected_at not scanned")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepo_Get_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewSessionRepo(db)

	mock.ExpectQuery("SELECT .* FROM wa_sessions WHERE id = .").
		WithArgs("missing").WillReturnError(noRows())

	_, err := repo.Get(context.Background(), "missing")
	assertNotFound(t, err)
}

func TestSessionRepo_UpdateStatus(t *testing.T) {
	db, mock := newMock(t)
	repo := NewSessionRepo(db)

	mock.ExpectExec("UPDATE wa_sessions SET status=., updated_at=. WHERE id=.").
		WithArgs(domain.SessionWorking, int64(999), "sess_1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateStatus(context.Background(), "sess_1", domain.SessionWorking, 999); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepo_UpdateStatus_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewSessionRepo(db)

	mock.ExpectExec("UPDATE wa_sessions SET status=").
		WithArgs(domain.SessionWorking, int64(999), "missing").
		WillReturnResult(sqlmock.NewResult(0, 0)) // zero rows affected -> not_found

	err := repo.UpdateStatus(context.Background(), "missing", domain.SessionWorking, 999)
	assertNotFound(t, err)
}

func TestSessionRepo_ListByTenant(t *testing.T) {
	db, mock := newMock(t)
	repo := NewSessionRepo(db)

	rows := sqlmock.NewRows(sessionColRow()).
		AddRow("sess_1", "ten_1", nil, "working", nil, nil, nil, false, true, false, 20, 200, nil, int64(100), int64(100)).
		AddRow("sess_2", "ten_1", nil, "stopped", nil, nil, nil, false, true, false, 20, 200, nil, int64(90), int64(90))
	mock.ExpectQuery("SELECT .* FROM wa_sessions WHERE tenant_id = . ORDER BY created_at DESC").
		WithArgs("ten_1").WillReturnRows(rows)

	got, err := repo.ListByTenant(context.Background(), "ten_1")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepo_Delete(t *testing.T) {
	db, mock := newMock(t)
	repo := NewSessionRepo(db)

	mock.ExpectExec("DELETE FROM wa_sessions WHERE id=.").
		WithArgs("sess_1").WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.Delete(context.Background(), "sess_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
