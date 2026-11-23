package store

import (
	"context"
	"errors"
	"testing"

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

	_, err := repo.IngestBatch(context.Background(), []GatewayEvent{ingestEvent("evt_1"), ingestEvent("evt_2")}, 999)
	if !errors.Is(err, ErrGatewayEventStale) {
		t.Fatalf("want ErrGatewayEventStale, got %v", err)
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
	mock.ExpectExec("SAVEPOINT gateway_event").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT IGNORE INTO gateway_ingested_events").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM event_log").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("INSERT INTO event_log").WithArgs(event.EventID, event.OrganizationID, event.SessionID, event.Type, []byte(`{"x":1}`), event.OccurredAt).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO session_event_sequences").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT last_seq FROM session_event_sequences").WillReturnRows(sqlmock.NewRows([]string{"last_seq"}).AddRow(1))
	mock.ExpectExec("INSERT INTO event_fanout").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := repo.Ingest(context.Background(), event, 999); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
