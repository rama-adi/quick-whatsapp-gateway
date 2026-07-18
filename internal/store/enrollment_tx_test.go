package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestReplaceLiveTokenMissingGatewayIsStateConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,status,deleted_at FROM gateways").WithArgs("gw_missing").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	err = NewEnrollmentStore(db).ReplaceLiveToken(context.Background(), "gw_missing", domain.EnrollmentToken{}, domain.AuditEvent{}, 1)
	if !errors.Is(err, ErrEnrollmentState) {
		t.Fatalf("error=%T %v", err, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeAndRecoveryLifecycleEligibility(t *testing.T) {
	for _, status := range []string{gatewayStatusActive, gatewayStatusDraining, gatewayStatusDrained} {
		t.Run(status, func(t *testing.T) {
			tx := &enrollmentTx{gatewayStatus: status}
			if tx.gatewayDuplicateFinalizeEligible(true) {
				t.Fatal("advanced lifecycle accepted for finalize")
			}
			if !tx.gatewayRecoveryEligible() {
				t.Fatal("advanced lifecycle rejected for consumed recovery")
			}
		})
	}
	joining := &enrollmentTx{gatewayStatus: gatewayStatusJoining}
	if joining.gatewayDuplicateFinalizeEligible(false) || !joining.gatewayDuplicateFinalizeEligible(true) {
		t.Fatal("joining must require a duplicate persisted issuance")
	}
	deleted := &enrollmentTx{gatewayStatus: gatewayStatusJoining, gatewayDeleted: true}
	if deleted.gatewayDuplicateFinalizeEligible(true) || deleted.gatewayRecoveryEligible() {
		t.Fatal("deleted gateway accepted")
	}
}
