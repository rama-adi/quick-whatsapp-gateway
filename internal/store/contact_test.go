package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func contactColRow() []string {
	return []string{
		"id", "session_id", "lid", "phone", "seen_in_dm", "dm_first_seen_at",
		"dm_last_seen_at", "message_count", "first_seen_at", "last_seen_at",
	}
}

func TestContactRepo_Upsert(t *testing.T) {
	db, mock := newMock(t)
	repo := NewContactRepo(db)

	c := domain.Contact{
		SessionID: "sess_1", LID: "6282144201954@s.whatsapp.net", Phone: domain.PhoneFromJID("6282144201954@s.whatsapp.net"),
		SeenInDM:      true,
		DMFirstSeenAt: i64ptr(10), DMLastSeenAt: i64ptr(20), MessageCount: 0,
		FirstSeenAt: 10, LastSeenAt: 20,
	}
	mock.ExpectExec("INSERT INTO whatsapp_contacts.*ON DUPLICATE KEY UPDATE").
		WithArgs(c.SessionID, c.LID, c.Phone, c.SeenInDM, c.DMFirstSeenAt, c.DMLastSeenAt, c.MessageCount, c.FirstSeenAt, c.LastSeenAt).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := repo.Upsert(context.Background(), c); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestContactRepo_BumpSeen(t *testing.T) {
	db, mock := newMock(t)
	repo := NewContactRepo(db)

	mock.ExpectExec("UPDATE whatsapp_contacts.*message_count = message_count . 1, last_seen_at = .").
		WithArgs(int64(50), "sess_1", "111@lid").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.BumpSeen(context.Background(), "sess_1", "111@lid", 50); err != nil {
		t.Fatalf("BumpSeen: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestContactRepo_Get_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewContactRepo(db)
	mock.ExpectQuery("SELECT .* FROM whatsapp_contacts WHERE session_id = . AND lid = .").
		WithArgs("sess_1", "x").WillReturnError(noRows())
	_, err := repo.Get(context.Background(), "sess_1", "x")
	assertNotFound(t, err)
}

func TestContactRepo_List_NoFilter(t *testing.T) {
	db, mock := newMock(t)
	repo := NewContactRepo(db)

	rows := sqlmock.NewRows(contactColRow()).
		AddRow(uint64(1), "sess_1", "a@lid", nil, true, int64(1), int64(2), int64(3), int64(1), int64(2)).
		AddRow(uint64(2), "sess_1", "6282144201954@s.whatsapp.net", "6282144201954", false, nil, nil, int64(0), int64(1), int64(2))
	// No joins, cursor 0, limit clamped to default.
	mock.ExpectQuery("SELECT c\\..* FROM whatsapp_contacts c WHERE c.session_id = . AND c.id > . ORDER BY c.id ASC LIMIT .").
		WithArgs("sess_1", uint64(0), defaultLimit).WillReturnRows(rows)

	page, err := repo.List(context.Background(), "sess_1", ContactFilter{}, "", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(page.Items))
	}
	// Partial page (2 < default limit) => no next cursor.
	if page.NextCursor != "" {
		t.Fatalf("want empty cursor, got %q", page.NextCursor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestContactRepo_List_GroupAndQFilter(t *testing.T) {
	db, mock := newMock(t)
	repo := NewContactRepo(db)

	rows := sqlmock.NewRows(contactColRow()).
		AddRow(uint64(3), "sess_1", "a@lid", nil, true, nil, nil, int64(5), int64(1), int64(2))
	// group filter -> JOIN group_members; q filter -> LEFT JOIN identities + LIKE.
	mock.ExpectQuery("FROM whatsapp_contacts c JOIN whatsapp_group_members gm.*LEFT JOIN whatsapp_identities i.*i.name LIKE .").
		WithArgs("12@g.us", "sess_1", uint64(2), "%alice%", 1).
		WillReturnRows(rows)

	page, err := repo.List(context.Background(), "sess_1",
		ContactFilter{Source: "group", GroupJID: "12@g.us", Q: "alice"}, "2", 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(page.Items))
	}
	// Full page (1 == limit 1) => next cursor is last id.
	if page.NextCursor != "3" {
		t.Fatalf("want cursor 3, got %q", page.NextCursor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestContactRepo_List_DMFilter(t *testing.T) {
	db, mock := newMock(t)
	repo := NewContactRepo(db)

	rows := sqlmock.NewRows(contactColRow())
	mock.ExpectQuery("FROM whatsapp_contacts c WHERE c.session_id = . AND c.id > . AND c.seen_in_dm = 1").
		WithArgs("sess_1", uint64(0), defaultLimit).WillReturnRows(rows)

	_, err := repo.List(context.Background(), "sess_1", ContactFilter{Source: "dm"}, "", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
