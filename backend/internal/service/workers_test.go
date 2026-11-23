package service

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

// TestRetentionWorkerPrune delegates one retention task to the store, preserving
// its safe deletion order and surfacing no error after every bounded phase
// succeeds. The repository has exhaustive batching and reference-preservation
// tests; this pins the queue consumer wiring.
func TestRetentionWorkerPrune(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	const cutoff = int64(1_000)
	mock.ExpectExec("DELETE FROM webhook_deliveries").
		WithArgs(cutoff, "delivered", "dead", int32(1000)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("DELETE FROM messages").
		WithArgs(cutoff, int32(1000)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("DELETE FROM event_log").
		WithArgs(cutoff, int32(1000)).
		WillReturnResult(sqlmock.NewResult(0, 4))

	worker := NewRetentionWorker(store.New(db), nil)
	require.NoError(t, worker.Prune(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}
