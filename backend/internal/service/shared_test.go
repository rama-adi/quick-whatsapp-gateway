package service

import (
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
	"testing"
)

// newStore wires a store over a sqlmock DB. The returned mock primes the
// session-ownership lookup every resource service performs first.
func newStore(t *testing.T) (*store.Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store.New(db), mock
}

// expectSession primes a SELECT … FROM wa_sessions returning a row owned by
// organizationID (or a different organization when owner != organizationID).
func expectSession(mock sqlmock.Sqlmock, sessionID, owner string) {
	cols := []string{
		"id", "organization_id", "created_by_user_id", "gateway_id", "label", "status",
		"wa_jid", "wa_lid", "phone_number", "is_admin_session", "auto_read", "presence_typing",
		"rate_per_min", "rate_per_hour", "last_connected_at", "created_at", "updated_at",
	}
	rows := sqlmock.NewRows(cols).AddRow(
		sessionID, owner, nil, "gw_1", nil, domain.SessionWorking, nil, nil, nil,
		false, false, false, 20, 200, nil, int64(1), int64(1),
	)
	mock.ExpectQuery("FROM wa_sessions").WithArgs(sessionID).WillReturnRows(rows)
}
