package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func TestWebhookDeliveryRepo_ClaimDueRollsBackOnLeaseFailure(t *testing.T) {
	db, mock := newMock(t)
	repo := NewWebhookDeliveryRepo(db)
	rows := sqlmock.NewRows([]string{
		"id", "webhook_id", "event_id", "status", "attempts", "response_code", "next_retry_at", "last_error", "created_at",
	}).AddRow(uint64(55), "wh_1", "evt_1", "pending", 0, nil, int64(90), nil, int64(100))

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .*FOR UPDATE SKIP LOCKED").
		WithArgs(domain.DeliveryPending, domain.DeliveryFailed, int64(200), 1).
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE webhook_deliveries").
		WithArgs(int64(200+webhookAttemptLeaseMs), uint64(55)).
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()

	if _, err := repo.ClaimDue(context.Background(), 200, 1); err == nil {
		t.Fatal("ClaimDue succeeded after lease failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
