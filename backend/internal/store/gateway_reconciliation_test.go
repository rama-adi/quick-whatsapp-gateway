package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectReconciliationFence(mock sqlmock.Sqlmock, gatewayID string, epoch, revision uint64, assignments *sqlmock.Rows) {
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT desired_revision FROM gateways").WithArgs(gatewayID, epoch).WillReturnRows(sqlmock.NewRows([]string{"desired_revision"}).AddRow(revision))
	mock.ExpectQuery("SELECT a.session_id").WithArgs(gatewayID).WillReturnRows(assignments)
}

// The inventory is a full set, not a hint: duplicate JIDs are rejected before
// any current reconciliation state can be replaced.
func TestGatewayReconciliationRejectsDuplicateInventory(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayReconciliationRepo(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT desired_revision FROM gateways").WithArgs("gw_1", uint64(3)).WillReturnRows(sqlmock.NewRows([]string{"desired_revision"}).AddRow(uint64(7)))
	mock.ExpectQuery("SELECT a.session_id").WithArgs("gw_1").WillReturnRows(sqlmock.NewRows([]string{"session_id", "assignment_epoch", "wa_jid", "run"}))
	mock.ExpectRollback()
	_, err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "healthy", LocalDevices: []string{"a@s.whatsapp.net", "a@s.whatsapp.net"}}, 100)
	if err == nil {
		t.Fatal("duplicate inventory accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayReconciliationRejectsAppliedRunningDeviceAbsentFromInventory(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayReconciliationRepo(db)
	expectReconciliationFence(mock, "gw_1", 3, 7, sqlmock.NewRows([]string{"session_id", "assignment_epoch", "wa_jid", "run"}).AddRow("s_1", uint64(4), "a@s.whatsapp.net", true))
	mock.ExpectRollback()
	session := "s_1"
	_, err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "healthy", Results: []GatewayReconciliationResult{{SessionID: &session, AssignmentEpoch: 4, DeviceJID: "a@s.whatsapp.net", Status: "applied"}}}, 100)
	if err == nil {
		t.Fatal("running applied result without inventory accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayReconciliationRejectsMissingDevicePresentInInventory(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayReconciliationRepo(db)
	expectReconciliationFence(mock, "gw_1", 3, 7, sqlmock.NewRows([]string{"session_id", "assignment_epoch", "wa_jid", "run"}).AddRow("s_1", uint64(4), "a@s.whatsapp.net", true))
	mock.ExpectRollback()
	session := "s_1"
	_, err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "missing", LocalDevices: []string{"a@s.whatsapp.net"}, Results: []GatewayReconciliationResult{{SessionID: &session, AssignmentEpoch: 4, DeviceJID: "a@s.whatsapp.net", Status: "keystore_missing"}}}, 100)
	if err == nil {
		t.Fatal("missing result with present device accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayReconciliationRejectsUnclassifiedInventory(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayReconciliationRepo(db)
	expectReconciliationFence(mock, "gw_1", 3, 7, sqlmock.NewRows([]string{"session_id", "assignment_epoch", "wa_jid", "run"}))
	mock.ExpectRollback()
	_, err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "healthy", LocalDevices: []string{"unknown@s.whatsapp.net"}}, 100)
	if err == nil {
		t.Fatal("unclassified inventory accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayReconciliationRejectsDuplicateOrForgedAssignedResults(t *testing.T) {
	tests := []struct {
		name      string
		results   []GatewayReconciliationResult
		inventory []string
	}{
		{name: "duplicate", inventory: []string{"a@s.whatsapp.net"}, results: []GatewayReconciliationResult{{SessionID: stringRef("s_1"), AssignmentEpoch: 4, DeviceJID: "a@s.whatsapp.net", Status: "applied"}, {SessionID: stringRef("s_1"), AssignmentEpoch: 4, DeviceJID: "a@s.whatsapp.net", Status: "applied"}}},
		{name: "forged session", inventory: []string{"a@s.whatsapp.net"}, results: []GatewayReconciliationResult{{SessionID: stringRef("s_forged"), AssignmentEpoch: 4, DeviceJID: "a@s.whatsapp.net", Status: "applied"}}},
		{name: "forged device", inventory: []string{"other@s.whatsapp.net"}, results: []GatewayReconciliationResult{{SessionID: stringRef("s_1"), AssignmentEpoch: 4, DeviceJID: "other@s.whatsapp.net", Status: "applied"}}},
		{name: "forged epoch", inventory: []string{"a@s.whatsapp.net"}, results: []GatewayReconciliationResult{{SessionID: stringRef("s_1"), AssignmentEpoch: 99, DeviceJID: "a@s.whatsapp.net", Status: "applied"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := newMock(t)
			repo := NewGatewayReconciliationRepo(db)
			expectReconciliationFence(mock, "gw_1", 3, 7, sqlmock.NewRows([]string{"session_id", "assignment_epoch", "wa_jid", "run"}).AddRow("s_1", uint64(4), "a@s.whatsapp.net", true))
			mock.ExpectRollback()
			_, err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "healthy", LocalDevices: tt.inventory, Results: tt.results}, 100)
			if err == nil {
				t.Fatal("forged or duplicate result accepted")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGatewayReconciliationCorruptKeystoreRemainsPresent(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayReconciliationRepo(db)
	expectReconciliationFence(mock, "gw_1", 3, 7, sqlmock.NewRows([]string{"session_id", "assignment_epoch", "wa_jid", "run"}))
	mock.ExpectExec("DELETE FROM gateway_reconciliation_results").WithArgs("gw_1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE gateways SET reconciliation_status").WithArgs("degraded", true, nil, "corrupt", nil, uint64(7), int64(100), "gw_1", uint64(3), uint64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	_, err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "corrupt"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A report for a revision that is no longer current is stale, not wrong: it
// is skipped without an error, so the control stream is not reset when, for
// example, a pairing attached its device while the gateway applied the
// previous snapshot.
func TestGatewayReconciliationSkipsSupersededRevision(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayReconciliationRepo(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT desired_revision FROM gateways").WithArgs("gw_1", uint64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"desired_revision"}).AddRow(uint64(8)))
	mock.ExpectRollback()
	persisted, err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "healthy"}, 100)
	if err != nil || persisted {
		t.Fatalf("superseded report: persisted=%v err=%v", persisted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A session that paired after its snapshot was built holds its new device
// while the row has none yet. The device belongs to the session; it is not an
// unexpected local device that would degrade the gateway.
func TestGatewayReconciliationAcceptsFreshPairingDevice(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayReconciliationRepo(db)
	expectReconciliationFence(mock, "gw_1", 3, 7, sqlmock.NewRows([]string{"session_id", "assignment_epoch", "wa_jid", "run"}).AddRow("s_1", uint64(4), "", true))
	mock.ExpectExec("DELETE FROM gateway_reconciliation_results").WithArgs("gw_1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO gateway_reconciliation_results").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE gateways SET reconciliation_status").WithArgs("healthy", true, nil, "healthy", nil, uint64(7), int64(100), "gw_1", uint64(3), uint64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	persisted, err := repo.Persist(context.Background(), GatewayReconciliationReport{
		GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "healthy",
		LocalDevices: []string{"new@s.whatsapp.net"},
		Results:      []GatewayReconciliationResult{{SessionID: stringRef("s_1"), AssignmentEpoch: 4, DeviceJID: "new@s.whatsapp.net", Status: "applied"}},
	}, 100)
	if err != nil || !persisted {
		t.Fatalf("fresh pairing report: persisted=%v err=%v", persisted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func stringRef(value string) *string { return &value }
