package store

import (
	"context"
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
