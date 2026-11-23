package service

import (
	"database/sql/driver"
	"fmt"
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
	cols := sessionTestColumns
	rows := sqlmock.NewRows(cols).AddRow(sessionTestRow(
		sessionID, owner, nil, "gw_1", nil, domain.SessionWorking, nil, nil, nil,
		false, false, false, 20, 200, nil, int64(1), int64(1), 30, nil, nil,
	)...)
	mock.ExpectQuery("FROM wa_sessions").WithArgs(sessionID).WillReturnRows(rows)
}

// sessionTestColumns is the column list of every session read (store
// sessionRow): intent columns, observed runtime, and gateway liveness.
var sessionTestColumns = []string{
	"id", "organization_id", "created_by_user_id", "gateway_id", "label", "desired_state",
	"start_generation", "wa_jid", "wa_lid", "phone_number", "is_admin_session", "auto_read",
	"presence_typing", "rate_per_min", "rate_per_hour", "last_connected_at", "created_at",
	"updated_at", "history_sync_days", "data_account_jid", "account_changed_at",
	"runtime_status", "runtime_failure_reason", "runtime_status_changed_at", "gateway_connected",
}

// sessionTestRow builds a session read row from the stored-row shape older
// tests use: status becomes the observed runtime status on a connected
// gateway, and a live status means desired state run.
func sessionTestRow(
	id, org, createdBy, gateway, label, status, waJID, waLID, phone, isAdmin, autoRead, presence,
	ratePerMin, ratePerHour, lastConnected, createdAt, updatedAt, historyDays, dataAccount, accountChanged any,
) []driver.Value {
	desired := "stop"
	if domain.SessionStatus(fmt.Sprint(status)).Live() {
		desired = "run"
	}
	return []driver.Value{
		id, org, createdBy, gateway, label, desired, uint64(0), waJID, waLID, phone, isAdmin, autoRead,
		presence, ratePerMin, ratePerHour, lastConnected, createdAt, updatedAt, historyDays, dataAccount,
		accountChanged, fmt.Sprint(status), nil, int64(1), true,
	}
}
