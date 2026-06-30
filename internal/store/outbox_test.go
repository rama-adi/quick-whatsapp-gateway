package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func outboxColRow() []string {
	return []string{
		"id", "organization_id", "session_id", "idempotency_key", "payload", "status",
		"attempts", "wa_message_id", "error", "created_at", "updated_at",
	}
}

func TestOutboxRepo_Insert(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)

	o := domain.OutboxEntry{
		ID: "out_1", OrganizationID: "ten_1", SessionID: "sess_1",
		IdempotencyKey: strptr("idem-1"), Payload: json.RawMessage(`{"type":"text"}`),
		Status: domain.OutboxQueued, CreatedAt: 100, UpdatedAt: 100,
	}
	mock.ExpectExec("INSERT INTO outbox").
		WithArgs(o.ID, o.OrganizationID, o.SessionID, o.IdempotencyKey, []byte(o.Payload),
			o.Status, o.Attempts, o.WAMessageID, o.Error, o.CreatedAt, o.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.Insert(context.Background(), o); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxRepo_GetByIdempotency(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)

	rows := sqlmock.NewRows(outboxColRow()).
		AddRow("out_1", "ten_1", "sess_1", "idem-1", []byte(`{"type":"text"}`),
			"sent", 1, "wamid_1", nil, int64(100), int64(200))
	mock.ExpectQuery("SELECT .* FROM outbox WHERE organization_id = . AND idempotency_key = .").
		WithArgs("ten_1", "idem-1").WillReturnRows(rows)

	got, err := repo.GetByIdempotency(context.Background(), "ten_1", "idem-1")
	if err != nil {
		t.Fatalf("GetByIdempotency: %v", err)
	}
	if got.ID != "out_1" || got.Status != domain.OutboxSent {
		t.Fatalf("unexpected entry: %+v", got)
	}
	if got.WAMessageID == nil || *got.WAMessageID != "wamid_1" {
		t.Fatalf("wa_message_id not scanned")
	}
	if string(got.Payload) != `{"type":"text"}` {
		t.Fatalf("payload not scanned: %s", got.Payload)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxRepo_GetByIdempotency_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)
	mock.ExpectQuery("SELECT .* FROM outbox WHERE organization_id = . AND idempotency_key = .").
		WithArgs("ten_1", "none").WillReturnError(noRows())
	_, err := repo.GetByIdempotency(context.Background(), "ten_1", "none")
	assertNotFound(t, err)
}

func TestOutboxRepo_UpdateStatus(t *testing.T) {
	// On success the payload's inline media bytes are stripped (JSON_REMOVE) so the
	// file content is not retained once the row is drained.
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)
	mock.ExpectExec("UPDATE outbox\\s+SET status = \\?, wa_message_id = \\?, error = \\?, updated_at = \\?,\\s+payload = JSON_REMOVE.*WHERE id = \\?").
		WithArgs(domain.OutboxSent, strptr("wamid_9"), (*string)(nil), int64(300), "out_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.UpdateStatus(context.Background(), "out_1", domain.OutboxSent, strptr("wamid_9"), nil, 300); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxRepo_UpdateStatus_FailedKeepsPayload(t *testing.T) {
	// A failed row keeps its payload (no JSON_REMOVE) so the async worker can retry.
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)
	mock.ExpectExec("UPDATE outbox\\s+SET status = \\?, wa_message_id = \\?, error = \\?, updated_at = \\?\\s+WHERE id = \\?").
		WithArgs(domain.OutboxFailed, (*string)(nil), strptr("boom"), int64(300), "out_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.UpdateStatus(context.Background(), "out_1", domain.OutboxFailed, nil, strptr("boom"), 300); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxRepo_ClaimQueued(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)

	// First the UPDATE marks the batch sending; then the SELECT reads it back.
	mock.ExpectExec("UPDATE outbox\\s+SET status = \\?, attempts = attempts \\+ 1, updated_at = \\?\\s+WHERE status = \\?\\s+ORDER BY created_at ASC\\s+LIMIT \\?").
		WithArgs(domain.OutboxSending, int64(777), domain.OutboxQueued, 5).
		WillReturnResult(sqlmock.NewResult(0, 2))

	rows := sqlmock.NewRows(outboxColRow()).
		AddRow("out_1", "ten_1", "sess_1", nil, []byte(`{}`), "sending", 1, nil, nil, int64(1), int64(777)).
		AddRow("out_2", "ten_1", "sess_1", nil, []byte(`{}`), "sending", 1, nil, nil, int64(2), int64(777))
	mock.ExpectQuery("SELECT .* FROM outbox\\s+WHERE status = \\? AND updated_at = \\?\\s+ORDER BY created_at ASC\\s+LIMIT \\?").
		WithArgs(domain.OutboxSending, int64(777), 5).WillReturnRows(rows)

	got, err := repo.ClaimQueued(context.Background(), 5, 777)
	if err != nil {
		t.Fatalf("ClaimQueued: %v", err)
	}
	if len(got) != 2 || got[0].ID != "out_1" {
		t.Fatalf("unexpected claim: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
