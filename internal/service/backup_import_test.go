package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/backup"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

const testNow = int64(1_700_000_000_000)

func newBackupSvc(t *testing.T) (*BackupImportService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := NewBackupImportService(store.New(db), nil)
	svc.clock = func() int64 { return testNow }
	return svc, mock
}

func sessionRows(id, orgID string) *sqlmock.Rows {
	cols := []string{
		"id", "organization_id", "created_by_user_id", "gateway_id", "label", "status",
		"wa_jid", "wa_lid", "phone_number", "is_admin_session", "auto_read", "presence_typing",
		"rate_per_min", "rate_per_hour", "last_connected_at", "created_at", "updated_at",
	}
	return sqlmock.NewRows(cols).AddRow(
		id, orgID, nil, "gw_1", nil, "working",
		nil, nil, nil, 0, 1, 0,
		20, 200, nil, int64(1), int64(1),
	)
}

func expectGetSession(mock sqlmock.Sqlmock, id, orgID string) {
	mock.ExpectQuery("SELECT .* FROM wa_sessions WHERE id = .").
		WithArgs(id).WillReturnRows(sessionRows(id, orgID))
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *domain.APIError, got %v", err)
	}
	return apiErr.Code
}

func TestStartImport_ForeignOrgNotFound(t *testing.T) {
	svc, mock := newBackupSvc(t)
	svc.decrypt = func([]byte, string) ([]byte, error) { t.Fatal("decrypt should not run"); return nil, nil }
	expectGetSession(mock, "sess_1", "org_2")

	_, err := svc.StartImport(context.Background(), "org_1", "sess_1", false, []byte("x"), "key")
	if got := apiCode(t, err); got != domain.CodeNotFound {
		t.Fatalf("want not_found, got %v", got)
	}
}

func TestStartImport_DecryptFails(t *testing.T) {
	svc, mock := newBackupSvc(t)
	svc.decrypt = func([]byte, string) ([]byte, error) { return nil, errors.New("bad key") }
	expectGetSession(mock, "sess_1", "org_1")

	_, err := svc.StartImport(context.Background(), "org_1", "sess_1", false, []byte("x"), "key")
	if got := apiCode(t, err); got != domain.CodeValidationError {
		t.Fatalf("want validation_error, got %v", got)
	}
}

func TestStartImport_Concurrency(t *testing.T) {
	svc, mock := newBackupSvc(t)
	svc.decrypt = func([]byte, string) ([]byte, error) { return []byte("db"), nil }
	expectGetSession(mock, "sess_1", "org_1")
	mock.ExpectQuery("SELECT 1 FROM backfill_imports WHERE session_id = . AND status = 'running'").
		WithArgs("sess_1", testNow-runningStaleMs).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	_, err := svc.StartImport(context.Background(), "org_1", "sess_1", false, []byte("x"), "key")
	if got := apiCode(t, err); got != domain.CodeConflict {
		t.Fatalf("want conflict, got %v", got)
	}
}

func TestStartImport_QuotaExceeded(t *testing.T) {
	svc, mock := newBackupSvc(t)
	svc.decrypt = func([]byte, string) ([]byte, error) { return []byte("db"), nil }
	expectGetSession(mock, "sess_1", "org_1")
	mock.ExpectQuery("SELECT 1 FROM backfill_imports WHERE session_id = . AND status = 'running'").
		WithArgs("sess_1", testNow-runningStaleMs).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT created_at FROM backfill_imports WHERE session_id = . AND status = 'succeeded'").
		WithArgs("sess_1").WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(testNow - 1000))

	_, err := svc.StartImport(context.Background(), "org_1", "sess_1", false, []byte("x"), "key")
	if got := apiCode(t, err); got != domain.CodeRateLimited {
		t.Fatalf("want rate_limited, got %v", got)
	}
}

func TestStartImport_SuperAdminBypassesQuotaAndOrg(t *testing.T) {
	svc, mock := newBackupSvc(t)
	svc.decrypt = func([]byte, string) ([]byte, error) { return []byte("db"), nil }
	// open never runs synchronously enough to assert; replace with a reader that
	// the (fire-and-forget) goroutine can use without touching the mock further.
	svc.open = func(string) (backupReader, error) { return &fakeReader{}, nil }
	// Cross-org session; caller is super_admin from a different org.
	expectGetSession(mock, "sess_1", "org_2")
	mock.ExpectQuery("SELECT 1 FROM backfill_imports WHERE session_id = . AND status = 'running'").
		WithArgs("sess_1", testNow-runningStaleMs).WillReturnError(sql.ErrNoRows)
	// No LastSuccessAt query expected (super_admin bypasses quota).
	mock.ExpectExec("INSERT INTO backfill_imports").WillReturnResult(sqlmock.NewResult(0, 1))

	job, err := svc.StartImport(context.Background(), "org_1", "sess_1", true, []byte("x"), "key")
	if err != nil {
		t.Fatalf("StartImport: %v", err)
	}
	if job.Status != "running" || job.OrganizationID != "org_2" {
		t.Fatalf("unexpected job: %+v", job)
	}
}

// fakeReader is a backupReader over in-memory DTOs.
type fakeReader struct {
	chats   []backup.Chat
	msgs    []backup.Message
	ids     []backup.Identity
	groups  []backup.Group
	members []backup.GroupMember
}

func (f *fakeReader) Fingerprint() string { return "build=1;caps=ab" }
func (f *fakeReader) Close() error        { return nil }
func (f *fakeReader) EachChat(_ context.Context, fn func(backup.Chat) error) error {
	for _, c := range f.chats {
		if err := fn(c); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeReader) EachMessage(_ context.Context, fn func(backup.Message) error) error {
	for _, m := range f.msgs {
		if err := fn(m); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeReader) EachIdentity(_ context.Context, fn func(backup.Identity) error) error {
	for _, i := range f.ids {
		if err := fn(i); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeReader) EachGroup(_ context.Context, fn func(backup.Group) error) error {
	for _, g := range f.groups {
		if err := fn(g); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeReader) EachGroupMember(_ context.Context, fn func(backup.GroupMember) error) error {
	for _, m := range f.members {
		if err := fn(m); err != nil {
			return err
		}
	}
	return nil
}

func TestImportAll_UpsertsAndCounts(t *testing.T) {
	svc, mock := newBackupSvc(t)
	reader := &fakeReader{
		ids:     []backup.Identity{{LID: "1@lid", Name: "Alice"}},
		groups:  []backup.Group{{JID: "120@g.us", Subject: "Team"}},
		members: []backup.GroupMember{{GroupJID: "120@g.us", LID: "1@lid", Role: "admin"}},
		chats:   []backup.Chat{{JID: "1@lid", Type: "dm", LastMessageAt: testNow}},
		msgs: []backup.Message{{
			WAMessageID: "WAMSG1", ChatJID: "1@lid", SenderLID: "1@lid",
			Type: "text", Body: "hi", TimestampMs: testNow,
		}},
	}
	mock.ExpectExec("INSERT INTO whatsapp_identities").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO whatsapp_groups").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO whatsapp_group_members").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO chats").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO messages").WillReturnResult(sqlmock.NewResult(0, 1))

	var job domain.BackfillImport
	if err := svc.importAll(context.Background(), reader, "sess_1", &job); err != nil {
		t.Fatalf("importAll: %v", err)
	}
	if job.Identities != 1 || job.Groups != 1 || job.GroupMembers != 1 || job.Chats != 1 || job.Messages != 1 {
		t.Fatalf("unexpected counts: %+v", job)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
