package service

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func historySessionRow(changedAt any) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "organization_id", "created_by_user_id", "gateway_id", "label", "status",
		"wa_jid", "wa_lid", "phone_number", "is_admin_session", "auto_read", "presence_typing",
		"rate_per_min", "rate_per_hour", "last_connected_at", "created_at", "updated_at",
		"history_sync_days", "data_account_jid", "account_changed_at",
	}).AddRow("sess_1", "org_1", nil, "gw_1", nil, domain.SessionWorking, nil, nil, nil,
		false, false, false, 20, 200, nil, int64(1), int64(1), 400, "62811@s.whatsapp.net", changedAt)
}

// TestSessionHistorySyncStatus reports the capped window, relink detection,
// and a stalled running run; no run yet is an empty status, not an error.
func TestSessionHistorySyncStatus(t *testing.T) {
	st, mock := newStore(t)
	svc := NewSessionService(st.Sessions, nil, nil)
	svc.SetHistorySync(st.History, 30, 365)

	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(historySessionRow(int64(5)))
	mock.ExpectQuery("FROM history_sync_runs").WithArgs("sess_1").WillReturnRows(sqlmock.NewRows([]string{
		"run_id", "account_jid", "status", "sync_type", "progress", "cutoff_at", "chunks", "batches",
		"conversations", "messages", "skipped", "batches_applied", "messages_imported", "messages_existing",
		"messages_dropped", "error", "started_at", "updated_at", "finished_at",
	}).AddRow("hsr_1", "62811@s.whatsapp.net", "running", "recent", 40, nil, 2, 4, 3, 300, 1, 4, 290, 10, 0, nil, int64(1), int64(2), nil))
	got, err := svc.HistorySync(context.Background(), "org_1", "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.HistorySyncDays != 365 || !got.AccountChanged || *got.AccountChangedAt != 5 || *got.AccountJID != "62811@s.whatsapp.net" {
		t.Fatalf("status = %+v", got)
	}
	if got.Run == nil || !got.Run.Stalled || got.Run.MessagesImported != 290 || *got.Run.Progress != 40 {
		t.Fatalf("run = %+v", got.Run)
	}

	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(historySessionRow(nil))
	mock.ExpectQuery("FROM history_sync_runs").WithArgs("sess_1").WillReturnRows(sqlmock.NewRows([]string{"run_id"}))
	got, err = svc.HistorySync(context.Background(), "org_1", "sess_1")
	if err != nil || got.Run != nil || got.AccountChanged {
		t.Fatalf("no-run status = %+v err=%v", got, err)
	}

	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(historySessionRow(nil))
	if _, err := svc.HistorySync(context.Background(), "org_other", "sess_1"); err == nil {
		t.Fatal("cross-org session must be not_found")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestResolveHistorySyncDays applies the default (capped) to an omitted value
// and rejects values outside 0..max.
func TestResolveHistorySyncDays(t *testing.T) {
	svc := &SessionService{}
	svc.SetHistorySync(nil, 30, 20)
	if got, err := svc.resolveHistorySyncDays(nil); err != nil || got != 20 {
		t.Fatalf("default = %d err=%v", got, err)
	}
	for _, v := range []int{-1, 21} {
		var apiErr *domain.APIError
		if _, err := svc.resolveHistorySyncDays(&v); !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
			t.Fatalf("%d: err=%v", v, err)
		}
	}
	zero := 0
	if got, err := svc.resolveHistorySyncDays(&zero); err != nil || got != 0 {
		t.Fatalf("zero = %d err=%v", got, err)
	}
}
