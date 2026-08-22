package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func ingestEvent(eventID string) GatewayEvent {
	return GatewayEvent{
		EventID: eventID, GatewayID: "gw_1", SessionID: "ses_1", OrganizationID: "org_1",
		Type: domain.EventMessage, ConnectionEpoch: 3, AssignmentEpoch: 7,
		Payload: []byte(`{"x":1}`), OccurredAt: 1234,
	}
}

// TestGatewayEventIngestBatch_DuplicateIsNoOp verifies acknowledgement-loss
// replay stays successful and never writes a second event_log row.
func TestGatewayEventIngestBatch_DuplicateIsNoOp(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayEventIngestRepo(db)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM gateway_session_assignments").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectExec("INSERT IGNORE INTO gateway_ingested_events").
		WillReturnResult(sqlmock.NewResult(0, 0)) // duplicate
	mock.ExpectCommit()

	if err := repo.Ingest(context.Background(), ingestEvent("evt_1"), 999); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGatewayEventIngestBatch_StaleFenceRejects verifies a fenced (stale epoch)
// batch rolls back entirely instead of acknowledging a partial set.
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

// TestClaimCommittedEvents_ReturnsEnvelopes verifies claiming reconstructs the
// durable envelope from event_log and leases each claimed row.
func TestClaimCommittedEvents_ReturnsEnvelopes(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayEventIngestRepo(db)

	mock.ExpectBegin()
	rows := sqlmock.NewRows([]string{"event_log_id", "type", "organization_id", "session_id", "created_at", "payload"}).
		AddRow("evt_1", domain.EventMessage, "org_1", "ses_1", int64(1234), []byte(`{"x":1}`))
	mock.ExpectQuery("SELECT i[.]event_log_id.*FROM gateway_ingested_events i JOIN event_log e").
		WithArgs(int64(2000), 8).
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE gateway_ingested_events SET claimed_by=., lease_until=.").
		WithArgs("api-1", int64(2000), "evt_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	events, err := repo.ClaimCommittedEvents(context.Background(), CommittedEventWork{
		Owner: "api-1", LeaseUntil: time.UnixMilli(2000), MaxItems: 8,
	})
	if err != nil {
		t.Fatalf("ClaimCommittedEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	got := events[0]
	payload, ok := got.Payload.(json.RawMessage)
	if !ok || string(payload) != `{"x":1}` {
		t.Fatalf("unexpected payload: %#v", got.Payload)
	}
	gotPayloadless := got
	gotPayloadless.Payload = nil
	if gotPayloadless != (domain.Event{Schema: domain.Schema, ID: "evt_1", Type: domain.EventMessage, Organization: "org_1", Session: "ses_1", Timestamp: 1234}) {
		t.Fatalf("unexpected envelope: %+v", gotPayloadless)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestClaimCommittedEvents_LostLeaseAbortsBatch verifies a claim that loses its
// row between select and lease aborts instead of returning a partially leased set.
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
		Owner: "api-1", LeaseUntil: time.UnixMilli(2000), MaxItems: 4,
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
