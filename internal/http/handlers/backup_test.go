package handlers

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
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

func (f *fakeBackupSvc) ImportStatus(_ context.Context, _, _ string, isSuperAdmin bool) (domain.BackfillImport, error) {
	f.gotSuperAdmin = isSuperAdmin
	if f.err != nil {
		return domain.BackfillImport{}, f.err
	}
	return f.status, nil
}

// backupRouter mounts the huma backup ops behind a principal-injecting middleware,
// mirroring how the assertion middleware populates the principal in production.
func backupRouter(svc BackupSvc, p *authz.Principal) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if p != nil {
				req = req.WithContext(authz.SetPrincipal(req.Context(), p))
			}
			next.ServeHTTP(w, req)
		})
	})
	api := humax.NewAPI(r)
	RegisterBackupOps(api, &Handlers{Backup: svc})
	return r
}

// multipartUploadReq builds a POST .../backfill request with the given file bytes
// and key field (omit either by passing false/empty).
func multipartUploadReq(t *testing.T, fileContent []byte, includeFile bool, key string) *http.Request {
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
	return r
}

func doBackupReq(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestImportBackup_HappyPath verifies the valid import backup flow and its observable contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestImportBackup_HappyPath(t *testing.T) {
	svc := &fakeBackupSvc{started: domain.BackfillImport{ID: "bfi_1", Status: "running"}}
	h := backupRouter(svc, manageOrgPrincipal())
	w := doBackupReq(h, multipartUploadReq(t, []byte("ciphertext"), true, "deadbeef"))

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if svc.gotKey != "deadbeef" || string(svc.gotData) != "ciphertext" {
		t.Fatalf("not threaded: key=%q data=%q", svc.gotKey, svc.gotData)
	}
	if svc.gotSuperAdmin {
		t.Errorf("expected non-super-admin for a plain owner principal")
	}
}

// TestImportBackup_SuperAdminThreaded verifies the import backup super admin threaded behavior remains part of the package contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestImportBackup_SuperAdminThreaded(t *testing.T) {
	svc := &fakeBackupSvc{started: domain.BackfillImport{ID: "bfi_1", Status: "running"}}
	h := backupRouter(svc, superAdminPrincipal())
	w := doBackupReq(h, multipartUploadReq(t, []byte("ciphertext"), true, "deadbeef"))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if !svc.gotSuperAdmin {
		t.Errorf("expected super-admin flag threaded for a super_admin principal")
	}
}

// TestImportBackup_NoPrincipal401 verifies unauthenticated callers are rejected with 401 before protected work runs.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestImportBackup_NoPrincipal401(t *testing.T) {
	h := backupRouter(&fakeBackupSvc{}, nil)
	w := doBackupReq(h, multipartUploadReq(t, []byte("x"), true, "key"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeUnauthorized {
		t.Errorf("code = %q, want %q", got, domain.CodeUnauthorized)
	}
}

// TestImportBackup_MissingCapability403 verifies callers lacking the required authority are rejected with 403.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestImportBackup_MissingCapability403(t *testing.T) {
	// A read-only api-key principal must not import backups.
	p := &authz.Principal{Kind: authz.KindAPIKey, OrganizationID: testOrganization, KeyPermissions: domain.Permissions{Read: true}}
	h := backupRouter(&fakeBackupSvc{}, p)
	w := doBackupReq(h, multipartUploadReq(t, []byte("x"), true, "key"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

// TestImportBackup_MissingKey400 verifies invalid input preserves the documented client-error mapping for import backup missing key400.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestImportBackup_MissingKey400(t *testing.T) {
	h := backupRouter(&fakeBackupSvc{}, manageOrgPrincipal())
	w := doBackupReq(h, multipartUploadReq(t, []byte("x"), true, ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeValidationError {
		t.Errorf("code = %q, want %q", got, domain.CodeValidationError)
	}
}

// TestImportBackup_MissingFile400 verifies invalid input preserves the documented client-error mapping for import backup missing file400.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestImportBackup_MissingFile400(t *testing.T) {
	h := backupRouter(&fakeBackupSvc{}, manageOrgPrincipal())
	w := doBackupReq(h, multipartUploadReq(t, nil, false, "key"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeValidationError {
		t.Errorf("code = %q, want %q", got, domain.CodeValidationError)
	}
}

// TestImportBackup_RateLimited429 verifies rate-limit denial preserves the public 429 response contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestImportBackup_RateLimited429(t *testing.T) {
	svc := &fakeBackupSvc{err: domain.ErrRateLimited("once per day")}
	h := backupRouter(svc, manageOrgPrincipal())
	w := doBackupReq(h, multipartUploadReq(t, []byte("x"), true, "key"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", w.Code, w.Body.String())
	}
}

// TestBackupStatus_HappyPath verifies the valid backup status flow and its observable contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestBackupStatus_HappyPath(t *testing.T) {
	svc := &fakeBackupSvc{status: domain.BackfillImport{ID: "bfi_1", Status: "succeeded", Messages: 100}}
	h := backupRouter(svc, manageOrgPrincipal())
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess_1/backfill", nil)
	w := doBackupReq(h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}
