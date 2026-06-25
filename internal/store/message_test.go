package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func messageColRow() []string {
	return []string{
		"id", "session_id", "wa_message_id", "chat_jid", "sender_lid", "sender_jid",
		"from_me", "direction", "type", "body", "quoted_message_id", "mentions",
		"has_media", "media_meta", "status", "ack_level", "error", "edited",
		"deleted", "timestamp", "raw_json", "created_at",
	}
}

func TestMessageRepo_Upsert(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	m := domain.Message{
		SessionID: "sess_1", WAMessageID: "wamid_1", ChatJID: "628@s.whatsapp.net",
		SenderLID: strptr("111@lid"), FromMe: false, Direction: domain.DirectionIn,
		Type: "text", Body: strptr("hi"), Mentions: json.RawMessage(`["a"]`),
		HasMedia: false, Timestamp: 1000, CreatedAt: 1000,
	}
	// Upsert must use ON DUPLICATE KEY UPDATE keyed on the unique (session,wamid).
	mock.ExpectExec("INSERT INTO messages.*ON DUPLICATE KEY UPDATE").
		WithArgs(
			m.SessionID, m.WAMessageID, m.ChatJID, m.SenderLID, m.SenderJID, m.FromMe,
			m.Direction, m.Type, m.Body, m.QuotedMessageID, []byte(m.Mentions),
			m.HasMedia, nil, m.Status, m.AckLevel, m.Error, m.Edited, m.Deleted,
			m.Timestamp, nil, m.CreatedAt,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := repo.Upsert(context.Background(), m); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMessageRepo_GetByWAID(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	media := []byte(`{"mimetype":"image/png","size":42,"filename":"a.png"}`)
	rows := sqlmock.NewRows(messageColRow()).
		AddRow(uint64(7), "sess_1", "wamid_1", "628@s.whatsapp.net", "111@lid", nil,
			false, "in", "image", "caption", nil, []byte(`["x"]`), true, media,
			"delivered", 2, nil, false, false, int64(1000), []byte(`{"k":1}`), int64(1000))
	mock.ExpectQuery("SELECT .* FROM messages WHERE session_id = . AND wa_message_id = .").
		WithArgs("sess_1", "wamid_1").WillReturnRows(rows)

	got, err := repo.GetByWAID(context.Background(), "sess_1", "wamid_1")
	if err != nil {
		t.Fatalf("GetByWAID: %v", err)
	}
	if got.ID != 7 || got.Type != "image" {
		t.Fatalf("unexpected message: %+v", got)
	}
	if got.MediaMeta == nil || got.MediaMeta.Mimetype != "image/png" || got.MediaMeta.Size != 42 {
		t.Fatalf("media_meta not scanned: %+v", got.MediaMeta)
	}
	if got.Status == nil || *got.Status != domain.MessageDelivered {
		t.Fatalf("status not scanned: %+v", got.Status)
	}
	if string(got.Mentions) != `["x"]` {
		t.Fatalf("mentions not scanned: %s", got.Mentions)
	}
	if string(got.RawJSON) != `{"k":1}` {
		t.Fatalf("raw_json not scanned: %s", got.RawJSON)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMessageRepo_GetByWAID_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)
	mock.ExpectQuery("SELECT .* FROM messages WHERE session_id = . AND wa_message_id = .").
		WithArgs("sess_1", "nope").WillReturnError(noRows())
	_, err := repo.GetByWAID(context.Background(), "sess_1", "nope")
	assertNotFound(t, err)
}

func TestMessageRepo_UpdateStatus(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	mock.ExpectExec("UPDATE messages SET status=., ack_level=., error=.").
		WithArgs(domain.MessageRead, intptr(3), (*string)(nil), "sess_1", "wamid_1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateStatus(context.Background(), "sess_1", "wamid_1", domain.MessageRead, intptr(3), nil); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMessageRepo_MarkEditedAndDeleted(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	mock.ExpectExec("UPDATE messages SET edited=1, body=. WHERE session_id=. AND wa_message_id=.").
		WithArgs("new", "sess_1", "wamid_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.MarkEdited(context.Background(), "sess_1", "wamid_1", "new"); err != nil {
		t.Fatalf("MarkEdited: %v", err)
	}

	mock.ExpectExec("UPDATE messages SET deleted=1 WHERE session_id=. AND wa_message_id=.").
		WithArgs("sess_1", "wamid_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.MarkDeleted(context.Background(), "sess_1", "wamid_1"); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMessageRepo_ListByChat_Pagination(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	// Page size 2, cursor "5" -> id > 5, limit 2; return exactly 2 -> next cursor
	// is the last id.
	rows := sqlmock.NewRows(messageColRow()).
		AddRow(uint64(6), "sess_1", "w6", "c", nil, nil, false, "in", "text", nil, nil, nil, false, nil, nil, nil, nil, false, false, int64(1), nil, int64(1)).
		AddRow(uint64(9), "sess_1", "w9", "c", nil, nil, false, "in", "text", nil, nil, nil, false, nil, nil, nil, nil, false, false, int64(2), nil, int64(2))
	mock.ExpectQuery("SELECT .* FROM messages WHERE session_id = . AND chat_jid = . AND id > . ORDER BY id ASC LIMIT .").
		WithArgs("sess_1", "c", uint64(5), 2).WillReturnRows(rows)

	page, err := repo.ListByChat(context.Background(), "sess_1", "c", "5", 2)
	if err != nil {
		t.Fatalf("ListByChat: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(page.Items))
	}
	if page.NextCursor != "9" {
		t.Fatalf("want next cursor 9, got %q", page.NextCursor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMessageRepo_ListByChat_BadCursor(t *testing.T) {
	db, _ := newMock(t)
	repo := NewMessageRepo(db)
	_, err := repo.ListByChat(context.Background(), "sess_1", "c", "notanumber", 10)
	if err == nil {
		t.Fatal("expected validation error for bad cursor")
	}
	var apiErr *domain.APIError
	if !asAPIError(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("want validation_error, got %v", err)
	}
}
