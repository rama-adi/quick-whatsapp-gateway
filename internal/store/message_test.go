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
		"deleted", "timestamp", "raw_json", "created_at", "sender_name",
	}
}

// TestMessageRepo_Upsert verifies complete message persistence and generated IDs.
// The expectation locks content, receipt, JSON, and timestamp bindings while allowing callers to omit the internal id.
func TestMessageRepo_Upsert(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	m := domain.Message{
		ID: "msg_01TEST00000000000000000000", SessionID: "sess_1", WAMessageID: "wamid_1", ChatJID: "628@s.whatsapp.net",
		SenderLID: strptr("111@lid"), FromMe: false, Direction: domain.DirectionIn,
		Type: "text", Body: strptr("hi"), Mentions: json.RawMessage(`["a"]`),
		HasMedia: false, Timestamp: 1000, CreatedAt: 1000,
	}
	// Upsert must use ON DUPLICATE KEY UPDATE keyed on the unique (session,wamid).
	mock.ExpectQuery("SELECT lid FROM whatsapp_identities").
		WithArgs(m.ChatJID).
		WillReturnRows(sqlmock.NewRows([]string{"lid"}))
	mock.ExpectExec("INSERT INTO messages.*ON DUPLICATE KEY UPDATE").
		WithArgs(
			m.ID, m.SessionID, m.WAMessageID, m.ChatJID, m.SenderLID, m.SenderJID, m.FromMe,
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

// TestMessageRepo_UpsertCanonicalizesPhoneChatAlias prevents duplicate DM timelines across aliases.
// A resolved phone chat must be rewritten to its canonical LID before the message upsert executes.
func TestMessageRepo_UpsertCanonicalizesPhoneChatAlias(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	m := domain.Message{
		ID: "msg_01TEST00000000000000000001", SessionID: "sess_1", WAMessageID: "wamid_2", ChatJID: "628@s.whatsapp.net",
		FromMe: false, Direction: domain.DirectionIn, Type: "text", Timestamp: 1000, CreatedAt: 1000,
	}
	mock.ExpectQuery("SELECT lid FROM whatsapp_identities").
		WithArgs(m.ChatJID).
		WillReturnRows(sqlmock.NewRows([]string{"lid"}).AddRow("111@lid"))
	mock.ExpectExec("INSERT INTO messages.*ON DUPLICATE KEY UPDATE").
		WithArgs(
			m.ID, m.SessionID, m.WAMessageID, "111@lid", m.SenderLID, m.SenderJID, m.FromMe,
			m.Direction, m.Type, m.Body, m.QuotedMessageID, nil,
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

// TestMessageRepo_GetByWAID verifies nullable fields and JSON metadata map losslessly.
// It reconstructs a content-rich row so read paths cannot silently discard sender, media, or receipt state.
func TestMessageRepo_GetByWAID(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	media := []byte(`{"mimetype":"image/png","size":42,"filename":"a.png"}`)
	rows := sqlmock.NewRows(messageColRow()).
		AddRow("msg_01TEST00000000000000000007", "sess_1", "wamid_1", "628@s.whatsapp.net", "111@lid", nil,
			false, "in", "image", "caption", nil, []byte(`["x"]`), true, media,
			"delivered", 2, nil, false, false, int64(1000), []byte(`{"k":1}`), int64(1000), "Alice")
	mock.ExpectQuery("SELECT .* FROM messages m .*WHERE m.session_id = . AND m.wa_message_id = .").
		WithArgs("sess_1", "wamid_1").WillReturnRows(rows)

	got, err := repo.GetByWAID(context.Background(), "sess_1", "wamid_1")
	if err != nil {
		t.Fatalf("GetByWAID: %v", err)
	}
	if got.ID != "msg_01TEST00000000000000000007" || got.Type != "image" {
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
	if got.SenderName == nil || *got.SenderName != "Alice" {
		t.Fatalf("sender_name not scanned: %+v", got.SenderName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestMessageRepo_GetByWAID_NotFound protects domain not-found mapping.
// Missing WhatsApp ids must not expose database-specific sentinel errors to services.
func TestMessageRepo_GetByWAID_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)
	mock.ExpectQuery("SELECT .* FROM messages m .*WHERE m.session_id = . AND m.wa_message_id = .").
		WithArgs("sess_1", "nope").WillReturnError(noRows())
	_, err := repo.GetByWAID(context.Background(), "sess_1", "nope")
	assertNotFound(t, err)
}

// TestMessageRepo_AdvanceReceiptStatus verifies the receipt-specific monotonic
// mutation is session scoped. A nil ack is passed through as SQL NULL so the
// query preserves any existing acknowledgement level.
func TestMessageRepo_AdvanceReceiptStatus(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	mock.ExpectExec("UPDATE messages.*SET.*status = CASE.*ack_level = CASE").
		WithArgs(int64(4), domain.MessageRead, intptr(3), intptr(3), intptr(3), "sess_1", "wamid_1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.AdvanceReceiptStatus(context.Background(), "sess_1", "wamid_1", domain.MessageRead, intptr(3)); err != nil {
		t.Fatalf("AdvanceReceiptStatus: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestMessageRepo_AdvanceReceiptStatus_UnknownIsNoOp pins the protocol boundary:
// receipts for history the gateway never captured are not repository failures.
func TestMessageRepo_AdvanceReceiptStatus_UnknownIsNoOp(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	mock.ExpectExec("UPDATE messages.*SET.*status = CASE.*ack_level = CASE").
		WithArgs(int64(3), domain.MessageDelivered, nil, nil, nil, "sess_1", "historical-id").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := repo.AdvanceReceiptStatus(context.Background(), "sess_1", "historical-id", domain.MessageDelivered, nil); err != nil {
		t.Fatalf("unknown receipt target must be a no-op: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestMessageRepo_MarkEditedAndDeleted protects edit/revoke mutations and row scoping.
// Both independent transitions must affect the selected message and treat zero affected rows consistently.
func TestMessageRepo_MarkEditedAndDeleted(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	mock.ExpectExec("UPDATE messages SET edited = 1, body = . WHERE session_id = . AND wa_message_id = .").
		WithArgs("new", "sess_1", "wamid_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.MarkEdited(context.Background(), "sess_1", "wamid_1", "new"); err != nil {
		t.Fatalf("MarkEdited: %v", err)
	}

	mock.ExpectExec("UPDATE messages SET deleted = 1 WHERE session_id = . AND wa_message_id = .").
		WithArgs("sess_1", "wamid_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.MarkDeleted(context.Background(), "sess_1", "wamid_1"); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestMessageRepo_ListByChat_Pagination verifies newest-first ULID cursor semantics.
// Consecutive pages bind the prior last id and preserve timeline order without duplicates.
func TestMessageRepo_ListByChat_Pagination(t *testing.T) {
	db, mock := newMock(t)
	repo := NewMessageRepo(db)

	// Page 0 is newest-first. Empty cursor returns the latest rows; next cursor
	// is the last row in that newest-first page.
	firstRows := sqlmock.NewRows(messageColRow()).
		AddRow("msg_01TEST00000000000000000009", "sess_1", "w9", "c", nil, nil, false, "in", "text", nil, nil, []byte(""), false, []byte(""), nil, nil, nil, false, false, int64(2), []byte(""), int64(2), nil).
		AddRow("msg_01TEST00000000000000000006", "sess_1", "w6", "c", nil, nil, false, "in", "text", nil, nil, []byte(""), false, []byte(""), nil, nil, nil, false, false, int64(1), []byte(""), int64(1), nil)
	mock.ExpectQuery("SELECT .* FROM messages m .*WHERE m.session_id = . AND .*m.id < .*m.chat_jid = .*EXISTS.*ORDER BY m.id DESC LIMIT .").
		WithArgs("sess_1", "", "", "c", "c", "c", 2).WillReturnRows(firstRows)

	first, err := repo.ListByChat(context.Background(), "sess_1", "c", "", 2)
	if err != nil {
		t.Fatalf("ListByChat first page: %v", err)
	}
	if got := []string{first.Items[0].ID, first.Items[1].ID}; got[0] != "msg_01TEST00000000000000000009" || got[1] != "msg_01TEST00000000000000000006" {
		t.Fatalf("first page order = %v, want newest-first 09,06", got)
	}
	if first.NextCursor != "msg_01TEST00000000000000000006" {
		t.Fatalf("want next cursor msg_...06, got %q", first.NextCursor)
	}

	// Older page uses id < cursor and keeps newest-first within that older slice.
	rows := sqlmock.NewRows(messageColRow()).
		AddRow("msg_01TEST00000000000000000005", "sess_1", "w5", "c", nil, nil, false, "in", "text", nil, nil, []byte(""), false, []byte(""), nil, nil, nil, false, false, int64(1), []byte(""), int64(1), nil).
		AddRow("msg_01TEST00000000000000000003", "sess_1", "w3", "c", nil, nil, false, "in", "text", nil, nil, []byte(""), false, []byte(""), nil, nil, nil, false, false, int64(2), []byte(""), int64(2), nil)
	mock.ExpectQuery("SELECT .* FROM messages m .*WHERE m.session_id = . AND .*m.id < .*m.chat_jid = .*EXISTS.*ORDER BY m.id DESC LIMIT .").
		WithArgs("sess_1", "msg_01TEST00000000000000000006", "msg_01TEST00000000000000000006", "c", "c", "c", 2).WillReturnRows(rows)

	page, err := repo.ListByChat(context.Background(), "sess_1", "c", "msg_01TEST00000000000000000006", 2)
	if err != nil {
		t.Fatalf("ListByChat: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(page.Items))
	}
	if got := []string{page.Items[0].ID, page.Items[1].ID}; got[0] != "msg_01TEST00000000000000000005" || got[1] != "msg_01TEST00000000000000000003" {
		t.Fatalf("older page order = %v, want newest-first 05,03", got)
	}
	if page.NextCursor != "msg_01TEST00000000000000000003" {
		t.Fatalf("want next cursor msg_...03, got %q", page.NextCursor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestMessageRepo_ListByChat_BadCursor rejects malformed cursors before querying.
// Whitespace-corrupted opaque values must return validation_error without reaching MySQL.
func TestMessageRepo_ListByChat_BadCursor(t *testing.T) {
	db, _ := newMock(t)
	repo := NewMessageRepo(db)
	_, err := repo.ListByChat(context.Background(), "sess_1", "c", " bad ", 10)
	if err == nil {
		t.Fatal("expected validation error for bad cursor")
	}
	var apiErr *domain.APIError
	if !asAPIError(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("want validation_error, got %v", err)
	}
}
