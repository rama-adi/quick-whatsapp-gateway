package store

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func outboxColRow() []string {
	return []string{
		"id", "organization_id", "session_id", "idempotency_key", "payload", "status",
		"attempts", "next_attempt_at", "wa_message_id", "error", "terminal_at",
		"created_at", "updated_at",
	}
}

func TestOutboxRepo_ClaimQueuedRollsBackOnCASFailure(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)
	rows := sqlmock.NewRows(outboxColRow()).
		AddRow("out_1", "ten_1", "sess_1", nil, []byte(`{}`), "queued", 0, int64(0), nil, nil, nil, int64(1), int64(1))

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .*FOR UPDATE SKIP LOCKED").
		WithArgs(domain.OutboxQueued, "", "", 1).WillReturnRows(rows)
	mock.ExpectExec("UPDATE outbox").
		WithArgs(domain.OutboxSending, int64(777), "out_1", domain.OutboxQueued, domain.OutboxFailed, domain.OutboxSending, int64(777)).
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()

	if _, err := repo.ClaimQueued(context.Background(), 1, 777); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ClaimQueued error = %v, want wrapped deadline", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestOutboxRepo_ClaimQueuedForSessionAppliesFilterBeforeLocking claims an empty page for one session.
// The session id must be bound inside the locking SELECT so rows from another account are never transitioned then discarded.
