package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func eventLogColRow() []string {
	return []string{"id", "event_id", "organization_id", "session_id", "type", "payload", "created_at"}
}

// TestEventLogRepo_Append verifies event envelopes are durably serialized with identity metadata.
// The insert must preserve exposed event id, tenant/session scope, type, payload, and creation time for replay.
func TestEventLogRepo_Append(t *testing.T) {
	db, mock := newMock(t)
	repo := NewEventLogRepo(db)

	e := domain.EventLogEntry{
		EventID: "evt_1", OrganizationID: "ten_1", SessionID: "sess_1",
		Type: domain.EventMessage, Payload: json.RawMessage(`{"x":1}`), CreatedAt: 100,
	}
	mock.ExpectExec("INSERT INTO event_log").
		WithArgs(e.EventID, e.OrganizationID, e.SessionID, e.Type, []byte(e.Payload), e.CreatedAt).
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

// TestEventLogRepo_ListSince_AllSessions verifies organization-wide replay ordering and bounds.
// With no session filter, events after the cursor must be returned in ascending durable-log order.
func TestEventLogRepo_ListSince_AllSessions(t *testing.T) {
	db, mock := newMock(t)
	repo := NewEventLogRepo(db)

	rows := sqlmock.NewRows(eventLogColRow()).
		AddRow(uint64(11), "evt_a", "ten_1", "sess_1", "message", []byte(`{}`), int64(1)).
		AddRow(uint64(12), "evt_b", "ten_1", "sess_2", "poll.vote", []byte(`{}`), int64(2))
	// No session filter -> organization-wide query, ordered by id ASC.
	mock.ExpectQuery("SELECT .* FROM event_log WHERE organization_id = . AND id > . ORDER BY id ASC LIMIT .").
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

// TestEventLogRepo_ListSince_SessionFilter verifies replay can be narrowed to one session.
// Both organization and session predicates are asserted so stream recovery cannot cross ownership boundaries.
func TestEventLogRepo_ListSince_SessionFilter(t *testing.T) {
	db, mock := newMock(t)
	repo := NewEventLogRepo(db)

	rows := sqlmock.NewRows(eventLogColRow()).
		AddRow(uint64(5), "evt_a", "ten_1", "sess_1", "message", []byte(`{"k":1}`), int64(1))
	mock.ExpectQuery("SELECT .* FROM event_log WHERE organization_id = . AND session_id = . AND id > . ORDER BY id ASC LIMIT .").
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

// TestEventLogRepo_GetByEventID_NotFound protects missing-envelope error mapping.
// Webhook dispatch relies on a domain not-found result to dead-letter irrecoverable missing events.
func TestEventLogRepo_GetByEventID_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewEventLogRepo(db)
	mock.ExpectQuery("SELECT .* FROM event_log WHERE event_id = .").
		WithArgs("missing").WillReturnError(noRows())
	_, err := repo.GetByEventID(context.Background(), "missing")
	assertNotFound(t, err)
}
