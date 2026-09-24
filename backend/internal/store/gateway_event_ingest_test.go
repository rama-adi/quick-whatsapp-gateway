package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func ingestEvent(eventID string) GatewayEvent {
	return GatewayEvent{
		EventID: eventID, GatewayID: "gw_1", SessionID: "ses_1", OrganizationID: "org_1",
		Type: domain.EventMessage, ConnectionEpoch: 3, AssignmentEpoch: 7,
		Payload: []byte(`{"x":1}`), OccurredAt: 1234,
	}
}

func TestGatewayEventIngestBatch_StaleFenceRejects(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayEventIngestRepo(db)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM gateway_session_assignments").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectRollback()

	err := repo.IngestBatch(context.Background(), []GatewayEvent{ingestEvent("evt_1"), ingestEvent("evt_2")}, 999)
	if !errors.Is(err, ErrGatewayEventStale) {
		t.Fatalf("want ErrGatewayEventStale, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimCommittedEvents_LostLeaseAbortsBatch(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayEventIngestRepo(db)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT i[.]event_log_id.*FROM gateway_ingested_events i JOIN event_log e").
		WillReturnRows(sqlmock.NewRows([]string{"event_log_id", "type", "organization_id", "session_id", "created_at", "payload"}).
			AddRow("evt_1", domain.EventMessage, "org_1", "ses_1", int64(1), []byte(`{}`)))
	mock.ExpectExec("UPDATE gateway_ingested_events SET claimed_by=., lease_until=.").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	if _, err := repo.ClaimCommittedEvents(context.Background(), CommittedEventWork{
		Owner: "api-1", ClaimedAt: time.UnixMilli(1000), LeaseUntil: time.UnixMilli(2000), MaxItems: 4,
	}); err == nil {
		t.Fatal("want error for lost claim")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCompleteCommittedEvent_FencedByOwner verifies completion only succeeds
// for the claiming owner, while an owner re-completing after a lost response is
// still a success.
func TestCompleteCommittedEvent_FencedByOwner(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayEventIngestRepo(db)

	const completeSelect = "SELECT \\(claimed_by = \\?\\) AND \\(completed_at IS NOT NULL\\) FROM gateway_ingested_events"

	// First: wrong owner. The CAS misses; the fallback shows a different owner.
	mock.ExpectExec("UPDATE gateway_ingested_events SET completed_at=").
		WithArgs(int64(5000), "evt_1", "api-2").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(completeSelect).
		WithArgs("api-2", "evt_1").
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(false))
	if err := repo.CompleteCommittedEvent(context.Background(), "api-2", "evt_1", time.UnixMilli(5000)); !errors.Is(err, errCommittedEventNotClaimed) {
		t.Fatalf("want errCommittedEventNotClaimed, got %v", err)
	}

	// Second: same owner retrying after a lost response is an idempotent success.
	mock.ExpectExec("UPDATE gateway_ingested_events SET completed_at=").
		WithArgs(int64(5001), "evt_1", "api-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(completeSelect).
		WithArgs("api-1", "evt_1").
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(true))
	if err := repo.CompleteCommittedEvent(context.Background(), "api-1", "evt_1", time.UnixMilli(5001)); err != nil {
		t.Fatalf("idempotent complete: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayEventIngestStripsPrivateMediaDescriptor(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayEventIngestRepo(db)
	event := ingestEvent("evt_private")
	event.Payload = []byte(`{"_mediaSource":"secret","x":1}`)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectExec("INSERT IGNORE INTO gateway_ingested_events").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO event_log").WithArgs(event.EventID, event.OrganizationID, event.SessionID, event.Type, []byte(`{"x":1}`), event.OccurredAt).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := repo.Ingest(context.Background(), event, 999); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
