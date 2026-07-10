package store

import (
	"context"
	"encoding/json"
	"errors"
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

// TestOutboxRepo_Insert verifies durable send payload and idempotency metadata binding.
// Every replay- and retry-critical field is asserted so a successful insert is sufficient to resume after restart.
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

// TestOutboxRepo_GetByIdempotency verifies organization-scoped replay lookup and JSON mapping.
// It ensures identical keys in another tenant cannot collide and the original payload/status are reconstructed.
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

// TestOutboxRepo_GetByIdempotency_NotFound protects the fresh-send not-found signal.
// sql.ErrNoRows must become a domain not-found error that the service can distinguish from storage failure.
func TestOutboxRepo_GetByIdempotency_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)
	mock.ExpectQuery("SELECT .* FROM outbox WHERE organization_id = . AND idempotency_key = .").
		WithArgs("ten_1", "none").WillReturnError(noRows())
	_, err := repo.GetByIdempotency(context.Background(), "ten_1", "none")
	assertNotFound(t, err)
}

// TestOutboxRepo_UpdateStatus verifies successful sends strip retained inline media.
// The generated update must remove sensitive bulk data only on the sent terminal path while recording the WA id.
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

// TestOutboxRepo_UpdateStatus_FailedKeepsPayload preserves retry material after failure.
// A failed transition must update attempts/error without deleting the payload needed by the next worker.
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

// TestOutboxRepo_ClaimQueued verifies bounded oldest-first queue selection.
// The transaction locks a deterministic page and CASes each row before commit, so replicas cannot return overlapping work.
func TestOutboxRepo_ClaimQueued(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)

	rows := sqlmock.NewRows(outboxColRow()).
		AddRow("out_1", "ten_1", "sess_1", nil, []byte(`{}`), "queued", 0, nil, nil, int64(1), int64(1)).
		AddRow("out_2", "ten_1", "sess_1", nil, []byte(`{}`), "queued", 2, nil, nil, int64(2), int64(2))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM outbox.*WHERE status = .*ORDER BY created_at ASC, id ASC.*FOR UPDATE SKIP LOCKED").
		WithArgs(domain.OutboxQueued, "", "", 5).WillReturnRows(rows)
	for _, id := range []string{"out_1", "out_2"} {
		mock.ExpectExec("UPDATE outbox.*status IN.*updated_at <=").
			WithArgs(domain.OutboxSending, int64(777), id, domain.OutboxQueued, domain.OutboxFailed, domain.OutboxSending, int64(777)).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	got, err := repo.ClaimQueued(context.Background(), 5, 777)
	if err != nil {
		t.Fatalf("ClaimQueued: %v", err)
	}
	if len(got) != 2 || got[0].ID != "out_1" || got[0].Status != domain.OutboxSending || got[0].Attempts != 1 || got[1].Attempts != 3 || got[1].UpdatedAt != 777 {
		t.Fatalf("unexpected claim: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestOutboxRepo_ClaimByIDCompareAndSet covers initial, failed-retry, active-lease, stale-lease, and terminal outcomes.
// Queued, failed, and cutoff-expired sending rows may be won; fresh sending and sent rows return normal false ownership.
func TestOutboxRepo_ClaimByIDCompareAndSet(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)

	for _, tc := range []struct {
		name    string
		id      string
		rows    int64
		claimed bool
	}{
		{name: "queued initial winner", id: "out_queued", rows: 1, claimed: true},
		{name: "failed retry winner", id: "out_failed", rows: 1, claimed: true},
		{name: "active sending lease", id: "out_active", rows: 0, claimed: false},
		{name: "stale sending recovery", id: "out_stale", rows: 1, claimed: true},
		{name: "sent terminal loser", id: "out_sent", rows: 0, claimed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock.ExpectExec("UPDATE outbox.*status IN.*updated_at <=").
				WithArgs(domain.OutboxSending, int64(900), tc.id, domain.OutboxQueued, domain.OutboxFailed, domain.OutboxSending, int64(800)).
				WillReturnResult(sqlmock.NewResult(0, tc.rows))
			claimed, err := repo.ClaimByID(context.Background(), tc.id, 900, 800)
			if err != nil || claimed != tc.claimed {
				t.Fatalf("ClaimByID() = (%v, %v), want (%v, nil)", claimed, err, tc.claimed)
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestOutboxRepo_ClaimByIDRejectsImpossibleRowCount simulates a corrupt driver result for a primary-key CAS.
// More than one affected row violates the ownership proof and must return an error rather than granting dispatch rights.
func TestOutboxRepo_ClaimByIDRejectsImpossibleRowCount(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)
	mock.ExpectExec("UPDATE outbox.*status IN.*updated_at <=").
		WithArgs(domain.OutboxSending, int64(900), "out_bad", domain.OutboxQueued, domain.OutboxFailed, domain.OutboxSending, int64(800)).
		WillReturnResult(sqlmock.NewResult(0, 2))

	if claimed, err := repo.ClaimByID(context.Background(), "out_bad", 900, 800); err == nil || claimed {
		t.Fatalf("ClaimByID() = (%v, %v), want false invariant error", claimed, err)
	}
}

// TestOutboxRepo_ClaimQueuedRollsBackOnCASFailure injects a write failure after rows are locked.
// The batch must roll back so no process observes a partially owned page and every queued row remains retryable.
func TestOutboxRepo_ClaimQueuedRollsBackOnCASFailure(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)
	rows := sqlmock.NewRows(outboxColRow()).
		AddRow("out_1", "ten_1", "sess_1", nil, []byte(`{}`), "queued", 0, nil, nil, int64(1), int64(1))

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
func TestOutboxRepo_ClaimQueuedForSessionAppliesFilterBeforeLocking(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOutboxRepo(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .*session_id = .*FOR UPDATE SKIP LOCKED").
		WithArgs(domain.OutboxQueued, "sess_1", "sess_1", 10).
		WillReturnRows(sqlmock.NewRows(outboxColRow()))
	mock.ExpectCommit()

	rows, err := repo.ClaimQueuedForSession(context.Background(), "sess_1", 10, 777)
	if err != nil || len(rows) != 0 {
		t.Fatalf("ClaimQueuedForSession() = (%v, %v), want empty success", rows, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
