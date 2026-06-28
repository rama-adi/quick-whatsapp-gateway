package handlers

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// --- Fake BackupSvc ---

type fakeBackupSvc struct {
	started       domain.BackfillImport
	status        domain.BackfillImport
	err           error
	gotKey        string
	gotData       []byte
	gotSuperAdmin bool
}

func (f *fakeBackupSvc) StartImport(_ context.Context, _, _ string, isSuperAdmin bool, data []byte, key string) (domain.BackfillImport, error) {
	f.gotKey, f.gotData, f.gotSuperAdmin = key, data, isSuperAdmin
	if f.err != nil {
		return domain.BackfillImport{}, f.err
	}
	return f.started, nil
}

func (f *fakeBackupSvc) ImportStatus(_ context.Context, _, _ string, _ bool) (domain.BackfillImport, error) {
	if f.err != nil {
		return domain.BackfillImport{}, f.err
	}
	return f.status, nil
}

// multipartReq builds a POST .../backfill request with the given file bytes and
// key field (omit either by passing nil/empty), with chi param session=sess_1.
func multipartReq(t *testing.T, fileContent []byte, includeFile bool, key string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if includeFile {
		fw, err := mw.CreateFormFile("file", "msgstore.db.crypt15")
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		_, _ = fw.Write(fileContent)
	}
	if key != "" {
		_ = mw.WriteField("key", key)
	}
	_ = mw.Close()

	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/sess_1/backfill", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("session", "sess_1")
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestImportBackup_HappyPath(t *testing.T) {
	svc := &fakeBackupSvc{started: domain.BackfillImport{ID: "bfi_1", Status: "running"}}
	h := &Handlers{Backup: svc}
	r := withOrganization(multipartReq(t, []byte("ciphertext"), true, "deadbeef"), testOrganization)
	w := httptest.NewRecorder()
	h.ImportBackup(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if svc.gotKey != "deadbeef" || string(svc.gotData) != "ciphertext" {
		t.Fatalf("not threaded: key=%q data=%q", svc.gotKey, svc.gotData)
	}
	if svc.gotSuperAdmin {
		t.Errorf("expected non-super-admin for an unauthenticated test principal")
	}
}

func TestImportBackup_NoOrganization401(t *testing.T) {
	h := &Handlers{Backup: &fakeBackupSvc{}}
	w := httptest.NewRecorder()
	h.ImportBackup(w, multipartReq(t, []byte("x"), true, "key"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestImportBackup_MissingKey400(t *testing.T) {
	h := &Handlers{Backup: &fakeBackupSvc{}}
	r := withOrganization(multipartReq(t, []byte("x"), true, ""), testOrganization)
	w := httptest.NewRecorder()
	h.ImportBackup(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestImportBackup_MissingFile400(t *testing.T) {
	h := &Handlers{Backup: &fakeBackupSvc{}}
	r := withOrganization(multipartReq(t, nil, false, "key"), testOrganization)
	w := httptest.NewRecorder()
	h.ImportBackup(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestImportBackup_RateLimited429(t *testing.T) {
	svc := &fakeBackupSvc{err: domain.ErrRateLimited("once per day")}
	h := &Handlers{Backup: svc}
	r := withOrganization(multipartReq(t, []byte("x"), true, "key"), testOrganization)
	w := httptest.NewRecorder()
	h.ImportBackup(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", w.Code, w.Body.String())
	}
}

func TestBackupStatus_HappyPath(t *testing.T) {
	svc := &fakeBackupSvc{status: domain.BackfillImport{ID: "bfi_1", Status: "succeeded", Messages: 100}}
	h := &Handlers{Backup: svc}
	r := withOrganization(chiReq(http.MethodGet, "/api/v1/sessions/sess_1/backfill", "", map[string]string{"session": "sess_1"}), testOrganization)
	w := httptest.NewRecorder()
	h.BackupStatus(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}
