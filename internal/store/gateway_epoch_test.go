package store

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestGatewayRepoAllocateConnectionUsesCASAndRetries(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)
	baseURL := "https://gateway.test"

	mock.ExpectQuery("SELECT connection_epoch.*FROM gateways").
		WithArgs("gw_1").
		WillReturnRows(sqlmock.NewRows([]string{"connection_epoch"}).AddRow(uint64(4)))
	mock.ExpectExec("UPDATE gateways.*connection_epoch = connection_epoch \\+ 1").
		WithArgs(int64(100), int64(100), baseURL, nil, nil, sqlmock.AnyArg(), 0, domain.GatewayJoining, int64(100), "gw_1", uint64(4)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT connection_epoch.*FROM gateways").
		WithArgs("gw_1").
		WillReturnRows(sqlmock.NewRows([]string{"connection_epoch"}).AddRow(uint64(5)))
	mock.ExpectExec("UPDATE gateways.*connection_epoch = connection_epoch \\+ 1").
		WithArgs(int64(100), int64(100), baseURL, nil, nil, sqlmock.AnyArg(), 0, domain.GatewayJoining, int64(100), "gw_1", uint64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT desired_lifecycle.*FROM gateways").
		WithArgs("gw_1", uint64(6)).
		WillReturnRows(sqlmock.NewRows([]string{"desired_lifecycle"}).AddRow("run"))

	accepted, err := repo.AcceptConnection(context.Background(), domain.GatewayConnectionHello{GatewayID: "gw_1", BaseURL: &baseURL, Status: domain.GatewayJoining}, 100)
	if err != nil || accepted.ConnectionEpoch != 6 || accepted.DesiredLifecycle != "run" {
		t.Fatalf("AcceptConnection: accepted=%+v err=%v", accepted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRepoAcceptReturnsPreservedAdministrativeDrain(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)

	mock.ExpectQuery("SELECT connection_epoch.*FROM gateways").
		WithArgs("gw_1").
		WillReturnRows(sqlmock.NewRows([]string{"connection_epoch"}).AddRow(uint64(8)))
	mock.ExpectExec("UPDATE gateways.*connection_epoch = connection_epoch \\+ 1").
		WithArgs(int64(100), int64(100), nil, nil, nil, sqlmock.AnyArg(), 2, domain.GatewayActive, int64(100), "gw_1", uint64(8)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT desired_lifecycle.*FROM gateways").
		WithArgs("gw_1", uint64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"desired_lifecycle"}).AddRow("drain"))

	accepted, err := repo.AcceptConnection(context.Background(), domain.GatewayConnectionHello{
		GatewayID: "gw_1", Status: domain.GatewayActive, SessionCount: 2,
	}, 100)
	if err != nil || accepted.ConnectionEpoch != 9 || accepted.DesiredLifecycle != "drain" {
		t.Fatalf("AcceptConnection: accepted=%+v err=%v", accepted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRepoLegacyShutdownDoesNotLatchControlRestartToDrain(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)

	mock.ExpectExec("UPDATE gateways\\s+SET status = \\?, updated_at = \\?").
		WithArgs(domain.GatewayDrained, int64(90), "gw_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.SetStatus(context.Background(), "gw_1", domain.GatewayDrained, 90); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT connection_epoch.*FROM gateways").
		WithArgs("gw_1").
		WillReturnRows(sqlmock.NewRows([]string{"connection_epoch"}).AddRow(uint64(2)))
	mock.ExpectExec("UPDATE gateways.*connection_epoch = connection_epoch \\+ 1").
		WithArgs(int64(100), int64(100), nil, nil, nil, sqlmock.AnyArg(), 0, domain.GatewayJoining, int64(100), "gw_1", uint64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT desired_lifecycle.*FROM gateways").
		WithArgs("gw_1", uint64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"desired_lifecycle"}).AddRow("run"))

	accepted, err := repo.AcceptConnection(context.Background(), domain.GatewayConnectionHello{
		GatewayID: "gw_1", Status: domain.GatewayJoining,
	}, 100)
	if err != nil || accepted.DesiredLifecycle != "run" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRepoAllocateConnectionRejectsIneligibleGateway(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)
	mock.ExpectQuery("SELECT connection_epoch.*FROM gateways").
		WithArgs("gw_disabled").
		WillReturnError(noRows())
	_, err := repo.AcceptConnection(context.Background(), domain.GatewayConnectionHello{GatewayID: "gw_disabled", Status: domain.GatewayJoining}, 100)
	assertNotFound(t, err)
}

func TestGatewayRepoFencedWritesReturnFalseForStaleEpoch(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)
	connection := domain.GatewayConnection{GatewayID: "gw_1", ConnectionEpoch: 9}

	mock.ExpectExec("UPDATE gateways.*last_seen_at = \\?, session_count = \\?,\\s+status = \\?").
		WithArgs(int64(200), 3, domain.GatewayDegraded, int64(200), "gw_1", uint64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("gw_1", uint64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"is_current"}).AddRow(false))
	applied, err := repo.HeartbeatForEpoch(context.Background(), domain.GatewayHeartbeat{
		GatewayConnection: connection, SessionCount: 3, Status: domain.GatewayDegraded,
	}, 200)
	if err != nil || applied {
		t.Fatalf("stale heartbeat: applied=%v err=%v", applied, err)
	}

	mock.ExpectExec("UPDATE gateways.*status = \\?").
		WithArgs(domain.GatewayDraining, int64(201), "gw_1", uint64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	applied, err = repo.SetStatusForEpoch(context.Background(), domain.GatewayLifecycleReport{
		GatewayConnection: connection, Status: domain.GatewayDraining,
	}, 201)
	if err != nil || !applied {
		t.Fatalf("current lifecycle: applied=%v err=%v", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRepoDisconnectClearsLivenessWithoutChangingLifecycle(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)
	connection := domain.GatewayConnection{GatewayID: "gw_1", ConnectionEpoch: 9}

	mock.ExpectExec("UPDATE gateways\\s+SET connected_at = NULL, last_seen_at = NULL, updated_at = \\?").
		WithArgs(int64(300), "gw_1", uint64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	applied, err := repo.DisconnectForEpoch(context.Background(), connection, 300)
	if err != nil || !applied {
		t.Fatalf("disconnect: applied=%v err=%v", applied, err)
	}

	mock.ExpectExec("UPDATE gateways\\s+SET connected_at = NULL, last_seen_at = NULL, updated_at = \\?").
		WithArgs(int64(301), "gw_1", uint64(8)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("gw_1", uint64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"is_current"}).AddRow(false))
	applied, err = repo.DisconnectForEpoch(context.Background(), domain.GatewayConnection{
		GatewayID: "gw_1", ConnectionEpoch: 8,
	}, 301)
	if err != nil || applied {
		t.Fatalf("stale disconnect: applied=%v err=%v", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRepoUnchangedCurrentEpochIsApplied(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)
	connection := domain.GatewayConnection{GatewayID: "gw_1", ConnectionEpoch: 9}

	mock.ExpectExec("UPDATE gateways.*status = \\?").
		WithArgs(domain.GatewayActive, int64(200), "gw_1", uint64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("gw_1", uint64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"is_current"}).AddRow(true))

	applied, err := repo.SetStatusForEpoch(context.Background(), domain.GatewayLifecycleReport{
		GatewayConnection: connection, Status: domain.GatewayActive,
	}, 200)
	if err != nil || !applied {
		t.Fatalf("unchanged current lifecycle: applied=%v err=%v", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRepoRuntimeReportDoesNotWriteDesiredLifecycle(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)
	connection := domain.GatewayConnection{GatewayID: "gw_1", ConnectionEpoch: 9}

	mock.ExpectExec("UPDATE gateways.*last_seen_at = \\?, session_count = \\?,\\s+status = \\?").
		WithArgs(int64(201), 3, domain.GatewayActive, int64(201), "gw_1", uint64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	applied, err := repo.HeartbeatForEpoch(context.Background(), domain.GatewayHeartbeat{
		GatewayConnection: connection, SessionCount: 3, Status: domain.GatewayActive,
	}, 201)
	if err != nil || !applied {
		t.Fatalf("guarded heartbeat: applied=%v err=%v", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

}

func TestGatewayRepoLifecycleCannotSetAdministrativeStates(t *testing.T) {
	db, _ := newMock(t)
	repo := NewGatewayRepo(db)
	for _, status := range []domain.GatewayStatus{
		domain.GatewayPendingEnrollment, domain.GatewayDisabled, domain.GatewayUnreachable,
	} {
		applied, err := repo.SetStatusForEpoch(context.Background(), domain.GatewayLifecycleReport{
			GatewayConnection: domain.GatewayConnection{GatewayID: "gw_1", ConnectionEpoch: 1},
			Status:            status,
		}, 1)
		if err == nil || applied {
			t.Errorf("status %q accepted: applied=%v err=%v", status, applied, err)
		}
	}
}

func TestGatewayRepoFencedMetadataValidatesIdentityAndJSON(t *testing.T) {
	db, _ := newMock(t)
	repo := NewGatewayRepo(db)
	connection := domain.GatewayConnection{GatewayID: "gw_1", ConnectionEpoch: 1}
	for _, metadata := range []domain.GatewayControlMetadata{
		{GatewayID: "gw_2"},
		{GatewayID: "gw_1", Capabilities: []byte("{")},
	} {
		applied, err := repo.UpdateConnectionMetadataForEpoch(context.Background(), connection, metadata, 1)
		if err == nil || applied {
			t.Fatalf("invalid metadata accepted: applied=%v err=%v", applied, err)
		}
	}
}

func TestGatewayRepoAllocateConnectionPropagatesDatabaseError(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)
	want := errors.New("database unavailable")
	mock.ExpectQuery("SELECT connection_epoch.*FROM gateways").
		WithArgs("gw_1").WillReturnError(want)
	_, err := repo.AcceptConnection(context.Background(), domain.GatewayConnectionHello{GatewayID: "gw_1", Status: domain.GatewayJoining}, 1)
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want wrapped %v", err, want)
	}
}
