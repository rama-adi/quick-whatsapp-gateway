package store

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestRetentionRepoPrune_BatchesAndPreservesActiveWebhookEvents pins the
// retention order and every safety boundary: terminal delivery history goes
// first, history messages are pruned by their WhatsApp timestamp, and event-log
// deletion excludes event IDs still required by pending or retryable deliveries.
// A full batch forces a second short DELETE rather than one unbounded statement.
func TestRetentionRepoPrune_BatchesAndPreservesActiveWebhookEvents(t *testing.T) {
	db, mock := newMock(t)
	repo := NewRetentionRepo(db)
	const cutoff = int64(1_000)

	mock.ExpectExec("DELETE FROM webhook_deliveries.*created_at < .+status IN .., ..*ORDER BY created_at, id.*LIMIT .").
		WithArgs(cutoff, "delivered", "dead", int32(retentionDeleteBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, int64(retentionDeleteBatchSize)))
	mock.ExpectExec("DELETE FROM webhook_deliveries.*created_at < .+status IN .., ..*ORDER BY created_at, id.*LIMIT .").
		WithArgs(cutoff, "delivered", "dead", int32(retentionDeleteBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 7))

	mock.ExpectExec("DELETE FROM messages.*timestamp < .*ORDER BY timestamp, id.*LIMIT .").
		WithArgs(cutoff, int32(retentionDeleteBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, int64(retentionDeleteBatchSize)))
	mock.ExpectExec("DELETE FROM messages.*timestamp < .*ORDER BY timestamp, id.*LIMIT .").
		WithArgs(cutoff, int32(retentionDeleteBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 4))

	// The correlated NOT EXISTS is the critical retention guard: webhook workers
	// reload the event body from event_log, so pending/failed deliveries retain it.
	mock.ExpectExec("(?s)DELETE FROM event_log.*NOT EXISTS.*FROM webhook_deliveries.*webhook_deliveries.event_id = event_log.event_id.*status IN .., ..*ORDER BY created_at, id.*LIMIT .").
		WithArgs(cutoff, "pending", "failed", int32(retentionDeleteBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, int64(retentionDeleteBatchSize)))
	mock.ExpectExec("(?s)DELETE FROM event_log.*NOT EXISTS.*FROM webhook_deliveries.*webhook_deliveries.event_id = event_log.event_id.*status IN .., ..*ORDER BY created_at, id.*LIMIT .").
		WithArgs(cutoff, "pending", "failed", int32(retentionDeleteBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 3))

	got, err := repo.Prune(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if want := int64(retentionDeleteBatchSize + 7); got.WebhookDeliveries != want {
		t.Fatalf("WebhookDeliveries = %d, want %d", got.WebhookDeliveries, want)
	}
	if want := int64(retentionDeleteBatchSize + 4); got.Messages != want {
		t.Fatalf("Messages = %d, want %d", got.Messages, want)
	}
	if want := int64(retentionDeleteBatchSize + 3); got.EventLog != want {
		t.Fatalf("EventLog = %d, want %d", got.EventLog, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRetentionRepoPrune_StopsOnDatabaseError ensures a transient DB failure is
// returned to Asynq for retry, and later phases do not run after the failed one.
func TestRetentionRepoPrune_StopsOnDatabaseError(t *testing.T) {
	db, mock := newMock(t)
	repo := NewRetentionRepo(db)
	const cutoff = int64(1_000)

	mock.ExpectExec("DELETE FROM webhook_deliveries").
		WithArgs(cutoff, "delivered", "dead", int32(retentionDeleteBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM messages").
		WithArgs(cutoff, int32(retentionDeleteBatchSize)).
		WillReturnError(errors.New("database unavailable"))

	got, err := repo.Prune(context.Background(), cutoff)
	if err == nil {
		t.Fatal("expected prune error")
	}
	if got.WebhookDeliveries != 1 || got.Messages != 0 || got.EventLog != 0 {
		t.Fatalf("partial result = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
