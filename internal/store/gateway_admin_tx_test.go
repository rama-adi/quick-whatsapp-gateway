package store

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func gatewayAdminAudit() GatewayAdminAudit {
	actor, gateway := "user_1", "gw_1"
	return GatewayAdminAudit{Event: domain.AuditEvent{ID: "aud_1", ActorType: "user", ActorID: &actor, Action: "gateway.disabled", ResourceType: "gateway", ResourceID: &gateway, Outcome: "success", CreatedAt: 100}}
}

func TestGatewayAdminDrainRejectsInvalidStateWithoutMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status,enrolled_at FROM gateways").WithArgs("gw_1").WillReturnRows(sqlmock.NewRows([]string{"status", "enrolled_at"}).AddRow("drained", 1))
	mock.ExpectRollback()
	err = NewGatewayAdminStore(db).Drain(context.Background(), "gw_1", 100, gatewayAdminAudit())
	if err != ErrGatewayAdminState {
		t.Fatalf("error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAdminReenrollRevokesBeforeIssuingAndAuditsAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	token := domain.EnrollmentToken{ID: "tok_1", GatewayID: "gw_1", TokenHash: make([]byte, 32), TokenPrefix: "enroll_", MaxAttempts: 5, ExpiresAt: 200, CreatedByUserID: "user_1", CreatedAt: 100, UpdatedAt: 100}
	audits := []GatewayAdminAudit{gatewayAdminAudit(), gatewayAdminAudit()}
	audits[1].Event.ID, audits[1].Event.Action = "aud_2", "enrollment.issued"
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status,enrolled_at FROM gateways").WithArgs("gw_1").WillReturnRows(sqlmock.NewRows([]string{"status", "enrolled_at"}).AddRow("disabled", 1))
	mock.ExpectExec("UPDATE gateway_enrollment_tokens SET status='revoked'").WithArgs(100, 100, "gw_1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE gateway_certificates SET revoked_at").WithArgs(100, "gw_1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE gateways SET status='pending_enrollment'").WithArgs(100, "gw_1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO gateway_enrollment_tokens").WithArgs(token.ID, token.GatewayID, token.TokenHash, token.TokenPrefix, token.MaxAttempts, token.ExpiresAt, token.CreatedByUserID, token.CreatedAt, token.UpdatedAt).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_events").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_events").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := NewGatewayAdminStore(db).Reenroll(context.Background(), "gw_1", token, 100, audits); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
