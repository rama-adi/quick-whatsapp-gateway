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
	err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "healthy", LocalDevices: []string{"a@s.whatsapp.net", "a@s.whatsapp.net"}}, 100)
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
	expectReconciliationFence(mock, "gw_1", 3, 7, sqlmock.NewRows([]string{"session_id", "wa_jid", "run"}).AddRow("s_1", "a@s.whatsapp.net", true))
	mock.ExpectRollback()
	session := "s_1"
	err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "healthy", Results: []GatewayReconciliationResult{{SessionID: &session, AssignmentEpoch: 4, DeviceJID: "a@s.whatsapp.net", Status: "applied"}}}, 100)
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
	expectReconciliationFence(mock, "gw_1", 3, 7, sqlmock.NewRows([]string{"session_id", "wa_jid", "run"}).AddRow("s_1", "a@s.whatsapp.net", true))
	mock.ExpectRollback()
	session := "s_1"
	err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "missing", LocalDevices: []string{"a@s.whatsapp.net"}, Results: []GatewayReconciliationResult{{SessionID: &session, AssignmentEpoch: 4, DeviceJID: "a@s.whatsapp.net", Status: "keystore_missing"}}}, 100)
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
	expectReconciliationFence(mock, "gw_1", 3, 7, sqlmock.NewRows([]string{"session_id", "wa_jid", "run"}))
	mock.ExpectRollback()
	err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "healthy", LocalDevices: []string{"unknown@s.whatsapp.net"}}, 100)
	if err == nil {
		t.Fatal("unclassified inventory accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayReconciliationRejectsDuplicateOrForgedAssignedResults(t *testing.T) {
	tests := []struct {
		name        string
		results     []GatewayReconciliationResult
		inventory   []string
		expectCount bool
	}{
		{name: "duplicate", inventory: []string{"a@s.whatsapp.net"}, expectCount: true, results: []GatewayReconciliationResult{{SessionID: stringRef("s_1"), AssignmentEpoch: 4, DeviceJID: "a@s.whatsapp.net", Status: "applied"}, {SessionID: stringRef("s_1"), AssignmentEpoch: 4, DeviceJID: "a@s.whatsapp.net", Status: "applied"}}},
		{name: "forged session", inventory: []string{"a@s.whatsapp.net"}, results: []GatewayReconciliationResult{{SessionID: stringRef("s_forged"), AssignmentEpoch: 4, DeviceJID: "a@s.whatsapp.net", Status: "applied"}}},
		{name: "forged device", inventory: []string{"other@s.whatsapp.net"}, results: []GatewayReconciliationResult{{SessionID: stringRef("s_1"), AssignmentEpoch: 4, DeviceJID: "other@s.whatsapp.net", Status: "applied"}}},
		{name: "forged epoch", inventory: []string{"a@s.whatsapp.net"}, expectCount: true, results: []GatewayReconciliationResult{{SessionID: stringRef("s_1"), AssignmentEpoch: 99, DeviceJID: "a@s.whatsapp.net", Status: "applied"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := newMock(t)
			repo := NewGatewayReconciliationRepo(db)
			expectReconciliationFence(mock, "gw_1", 3, 7, sqlmock.NewRows([]string{"session_id", "wa_jid", "run"}).AddRow("s_1", "a@s.whatsapp.net", true))
			if tt.expectCount {
				epoch := tt.results[0].AssignmentEpoch
				count := 0
				if tt.name == "duplicate" {
					count = 1 // first result passes; the second must trip the duplicate guard.
				}
				mock.ExpectQuery("SELECT COUNT.*gateway_session_assignments").WithArgs("gw_1", "s_1", epoch, "a@s.whatsapp.net", "a@s.whatsapp.net").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
			}
			mock.ExpectRollback()
			err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "healthy", LocalDevices: tt.inventory, Results: tt.results}, 100)
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
	expectReconciliationFence(mock, "gw_1", 3, 7, sqlmock.NewRows([]string{"session_id", "wa_jid", "run"}))
	mock.ExpectExec("DELETE FROM gateway_reconciliation_results").WithArgs("gw_1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE gateways SET reconciliation_status").WithArgs("degraded", true, nil, "corrupt", nil, uint64(7), int64(100), "gw_1", uint64(3), uint64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	err := repo.Persist(context.Background(), GatewayReconciliationReport{GatewayID: "gw_1", Epoch: 3, Revision: 7, KeystoreState: "corrupt"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func stringRef(value string) *string { return &value }
