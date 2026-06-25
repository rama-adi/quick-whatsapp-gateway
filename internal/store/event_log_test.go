package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func eventLogColRow() []string {
	return []string{"id", "event_id", "tenant_id", "session_id", "type", "payload", "created_at"}
}

func TestEventLogRepo_Append(t *testing.T) {
	db, mock := newMock(t)
	repo := NewEventLogRepo(db)

	e := domain.EventLogEntry{
		EventID: "evt_1", TenantID: "ten_1", SessionID: "sess_1",
		Type: domain.EventMessage, Payload: json.RawMessage(`{"x":1}`), CreatedAt: 100,
	}
	mock.ExpectExec("INSERT INTO event_log").
		WithArgs(e.EventID, e.TenantID, e.SessionID, e.Type, []byte(e.Payload), e.CreatedAt).
		WillReturnResult(sqlmock.NewResult(42, 1))

	id, err := repo.Append(context.Background(), e)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if id != 42 {
		t.Fatalf("want id 42, got %d", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEventLogRepo_ListSince_AllSessions(t *testing.T) {
	db, mock := newMock(t)
	repo := NewEventLogRepo(db)

	rows := sqlmock.NewRows(eventLogColRow()).
		AddRow(uint64(11), "evt_a", "ten_1", "sess_1", "message", []byte(`{}`), int64(1)).
		AddRow(uint64(12), "evt_b", "ten_1", "sess_2", "poll.vote", []byte(`{}`), int64(2))
	// No session filter -> tenant-wide query, ordered by id ASC.
	mock.ExpectQuery("SELECT .* FROM event_log WHERE tenant_id = . AND id > . ORDER BY id ASC LIMIT .").
		WithArgs("ten_1", uint64(10), 100).WillReturnRows(rows)

	got, err := repo.ListSince(context.Background(), "ten_1", "", 10, 100)
	if err != nil {
		t.Fatalf("ListSince: %v", err)
	}
	if len(got) != 2 || got[0].ID != 11 || got[1].EventID != "evt_b" {
		t.Fatalf("unexpected rows: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEventLogRepo_ListSince_SessionFilter(t *testing.T) {
	db, mock := newMock(t)
	repo := NewEventLogRepo(db)

	rows := sqlmock.NewRows(eventLogColRow()).
		AddRow(uint64(5), "evt_a", "ten_1", "sess_1", "message", []byte(`{"k":1}`), int64(1))
	mock.ExpectQuery("SELECT .* FROM event_log WHERE tenant_id = . AND session_id = . AND id > . ORDER BY id ASC LIMIT .").
		WithArgs("ten_1", "sess_1", uint64(0), defaultLimit).WillReturnRows(rows)

	got, err := repo.ListSince(context.Background(), "ten_1", "sess_1", 0, 0)
	if err != nil {
		t.Fatalf("ListSince: %v", err)
	}
	if len(got) != 1 || string(got[0].Payload) != `{"k":1}` {
		t.Fatalf("payload not scanned: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEventLogRepo_GetByEventID_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewEventLogRepo(db)
	mock.ExpectQuery("SELECT .* FROM event_log WHERE event_id = .").
		WithArgs("missing").WillReturnError(noRows())
	_, err := repo.GetByEventID(context.Background(), "missing")
	assertNotFound(t, err)
}
