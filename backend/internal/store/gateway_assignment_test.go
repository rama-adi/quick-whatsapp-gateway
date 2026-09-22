package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGatewayAssignmentReassignFencesBothGatewaysAtomically(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayAssignmentRepo(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT gateway_id, assignment_epoch FROM gateway_session_assignments").WithArgs("session_1").WillReturnRows(sqlmock.NewRows([]string{"gateway_id", "assignment_epoch"}).AddRow("gw_old", uint64(4)))
	mock.ExpectQuery("SELECT COUNT.*FROM gateways").WithArgs("gw_new").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec("UPDATE gateway_session_assignments SET gateway_id=.*assignment_epoch=assignment_epoch\\+1").WithArgs("gw_new", int64(100), "session_1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE wa_sessions SET gateway_id").WithArgs("gw_new", int64(100), "session_1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE gateways SET desired_revision=desired_revision\\+1").WithArgs(int64(100), "gw_old", "gw_new").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	epoch, err := repo.Reassign(context.Background(), "session_1", "gw_new", 100)
	if err != nil || epoch != 5 {
		t.Fatalf("Reassign = %d, %v", epoch, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAssignmentUpdateConfigRejectsNegativeRatesBeforeTransaction(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayAssignmentRepo(db)
	if err := repo.UpdateConfig(context.Background(), "session_1", true, false, -1, 20, 100); err == nil {
		t.Fatal("negative rate accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAssignmentReassignRejectsIneligibleTarget(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayAssignmentRepo(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT gateway_id, assignment_epoch FROM gateway_session_assignments").WithArgs("session_1").WillReturnRows(sqlmock.NewRows([]string{"gateway_id", "assignment_epoch"}).AddRow("gw_old", uint64(4)))
	mock.ExpectQuery("SELECT COUNT.*FROM gateways").WithArgs("gw_disabled").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectRollback()
	if _, err := repo.Reassign(context.Background(), "session_1", "gw_disabled", 100); err == nil {
		t.Fatal("ineligible target accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAssignmentReassignRollsBackWhenBothRevisionsAreNotAdvanced(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayAssignmentRepo(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT gateway_id, assignment_epoch FROM gateway_session_assignments").WithArgs("session_1").WillReturnRows(sqlmock.NewRows([]string{"gateway_id", "assignment_epoch"}).AddRow("gw_old", uint64(4)))
	mock.ExpectQuery("SELECT COUNT.*FROM gateways").WithArgs("gw_new").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec("UPDATE gateway_session_assignments SET gateway_id=.*assignment_epoch=assignment_epoch\\+1").WithArgs("gw_new", int64(100), "session_1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE wa_sessions SET gateway_id").WithArgs("gw_new", int64(100), "session_1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE gateways SET desired_revision=desired_revision\\+1").WithArgs(int64(100), "gw_old", "gw_new").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()
	if _, err := repo.Reassign(context.Background(), "session_1", "gw_new", 100); err == nil {
		t.Fatal("partial revision advancement accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAssignmentAssignInsertsEpochOneAndAdvancesRevision(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayAssignmentRepo(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT assignment_epoch FROM gateway_session_assignments").WithArgs("session_9").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT COUNT.*FROM gateways").WithArgs("gw_1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec("INSERT INTO gateway_session_assignments \\(session_id, gateway_id, assignment_epoch, created_at, updated_at\\) VALUES \\(\\?, \\?, 1, \\?, \\?\\)").WithArgs("session_9", "gw_1", int64(100), int64(100)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE gateways SET desired_revision=desired_revision\\+1").WithArgs(int64(100), "gw_1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	epoch, err := repo.Assign(context.Background(), "session_9", "gw_1", 100)
	if err != nil || epoch != 1 {
		t.Fatalf("Assign = %d, %v", epoch, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAssignmentAssignKeepsExistingAssignment(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayAssignmentRepo(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT assignment_epoch FROM gateway_session_assignments").WithArgs("session_9").WillReturnRows(sqlmock.NewRows([]string{"assignment_epoch"}).AddRow(uint64(7)))
	mock.ExpectCommit()
	epoch, err := repo.Assign(context.Background(), "session_9", "gw_2", 100)
	if err != nil || epoch != 7 {
		t.Fatalf("Assign = %d, %v (existing assignment must be kept)", epoch, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAssignmentUnassignDeletesAndAdvancesRevision(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayAssignmentRepo(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT gateway_id FROM gateway_session_assignments").WithArgs("session_9").WillReturnRows(sqlmock.NewRows([]string{"gateway_id"}).AddRow("gw_1"))
	mock.ExpectExec("DELETE FROM gateway_session_assignments WHERE session_id=\\?").WithArgs("session_9").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE gateways SET desired_revision=desired_revision\\+1").WithArgs(int64(200), "gw_1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := repo.Unassign(context.Background(), "session_9", 200); err != nil {
		t.Fatalf("Unassign: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAssignmentUnassignTreatsMissingRowAsReconciled(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayAssignmentRepo(db)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT gateway_id FROM gateway_session_assignments").WithArgs("session_404").WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()
	if err := repo.Unassign(context.Background(), "session_404", 200); err != nil {
		t.Fatalf("Unassign missing row: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
