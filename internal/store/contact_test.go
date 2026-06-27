package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// contactProjRow is the column set returned by the identity-centric List query.
func contactProjRow() []string {
	return []string{"id", "lid", "phone_number", "name", "business_name", "in_dm", "in_group"}
}

func TestContactRepo_List_Anywhere(t *testing.T) {
	db, mock := newMock(t)
	repo := NewContactRepo(db)

	rows := sqlmock.NewRows(contactProjRow()).
		AddRow(uint64(1), "a@lid", nil, "Alice", nil, true, false).
		AddRow(uint64(2), "6282144201954@lid", "6282144201954", nil, "Biz", false, true)
	// Default ("anywhere"): SELECT exists(dm), exists(group); WHERE id>? AND (dm OR group).
	mock.ExpectQuery("SELECT i.id.*FROM whatsapp_identities i WHERE i.id > .").
		WithArgs("sess_1", "sess_1", uint64(0), "sess_1", "sess_1", defaultLimit).
		WillReturnRows(rows)

	page, err := repo.List(context.Background(), "sess_1", ContactFilter{}, "", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(page.Items))
	}
	if page.Items[0].Source != "dm" {
		t.Fatalf("row seen in dm should have source dm, got %q", page.Items[0].Source)
	}
	if page.Items[1].Source != "group" {
		t.Fatalf("row seen only in group should have source group, got %q", page.Items[1].Source)
	}
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

	rows := sqlmock.NewRows(contactProjRow()).
		AddRow(uint64(3), "a@lid", nil, "Alice", nil, false, true)
	mock.ExpectQuery("FROM whatsapp_identities i WHERE i.id > ..*whatsapp_group_members gm.*group_jid = ..*i.name LIKE .").
		WithArgs("sess_1", "sess_1", uint64(2), "sess_1", "12@g.us", "%alice%", 1).
		WillReturnRows(rows)

	page, err := repo.List(context.Background(), "sess_1",
		ContactFilter{Source: "group", GroupJID: "12@g.us", Q: "alice"}, "2", 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Source != "group" {
		t.Fatalf("unexpected items: %+v", page.Items)
	}
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

	rows := sqlmock.NewRows(contactProjRow())
	// DM filter: SELECT exists(dm), exists(group); WHERE id>? AND exists(dm).
	mock.ExpectQuery("FROM whatsapp_identities i WHERE i.id > .").
		WithArgs("sess_1", "sess_1", uint64(0), "sess_1", defaultLimit).
		WillReturnRows(rows)

	if _, err := repo.List(context.Background(), "sess_1", ContactFilter{Source: "dm"}, "", 0); err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestContactRepo_SeenInDM(t *testing.T) {
	db, mock := newMock(t)
	repo := NewContactRepo(db)

	rows := sqlmock.NewRows([]string{"found"}).AddRow(true)
	mock.ExpectQuery("SELECT EXISTS .*FROM chats.*type = 'dm'").
		WithArgs("sess_1", "111@lid", "628111@s.whatsapp.net", "628111@s.whatsapp.net").
		WillReturnRows(rows)

	got, err := repo.SeenInDM(context.Background(), "sess_1", "111@lid", "628111@s.whatsapp.net")
	if err != nil {
		t.Fatalf("SeenInDM: %v", err)
	}
	if !got {
		t.Fatal("want true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
