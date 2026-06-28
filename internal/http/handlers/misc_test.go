package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

func TestParam_DecodesEncodedJID(t *testing.T) {
	// chi returns the still-escaped path segment when URL.RawPath is set; param
	// must URL-decode it so a JID like "120363@g.us" (arriving as "120363%40g.us")
	// matches the stored value.
	r := chiReq(http.MethodGet, "/", "", map[string]string{
		"cid":     "120363025249719889%40g.us",
		"session": "sess_plain",
	})
	if got := param(r, "cid"); got != "120363025249719889@g.us" {
		t.Errorf("cid = %q, want decoded @", got)
	}
	if got := param(r, "session"); got != "sess_plain" {
		t.Errorf("session = %q, want unchanged", got)
	}
}

// superAdminPrincipal is a JWT super_admin platform principal.
func superAdminPrincipal() *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, OrganizationID: testOrganization, PlatformRole: authz.PlatformRoleSuperAdmin}
}

// adminRouter builds a chi router with the huma admin ops mounted behind a
// middleware injecting the given principal (nil = unauthenticated).
func adminRouter(svc AdminSvc, p *authz.Principal) http.Handler {
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
	RegisterAdminOps(api, &Handlers{Admin: svc})
	return r
}

func TestAdminListSessions_HappyPath(t *testing.T) {
	svc := &fakeAdminSvc{list: []domain.WASession{{ID: "sess_1"}, {ID: "sess_2"}}}
	h := adminRouter(svc, superAdminPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/admin/sessions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data []domain.WASession `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data) != 2 || env.Data[0].ID != "sess_1" {
		t.Errorf("data = %+v, want sess_1, sess_2", env.Data)
	}
}

func TestAdminListSessions_NoPrincipal401(t *testing.T) {
	h := adminRouter(&fakeAdminSvc{}, nil)
	w := doReq(h, http.MethodGet, "/api/v1/admin/sessions", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeUnauthorized {
		t.Errorf("code = %q, want %q", got, domain.CodeUnauthorized)
	}
}

func TestAdminListSessions_NonSuperAdmin403(t *testing.T) {
	// An org owner (manage) must not reach the admin oversight surface.
	h := adminRouter(&fakeAdminSvc{}, manageOrgPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/admin/sessions", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeForbidden {
		t.Errorf("code = %q, want %q", got, domain.CodeForbidden)
	}
}

func TestAdminStartSessionBackfill_202_ColonRoute(t *testing.T) {
	svc := &fakeAdminSvc{job: domain.BackfillJob{ID: "job_1", SessionID: "sess_1", Status: "running"}}
	h := adminRouter(svc, superAdminPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/admin/sessions/sess_1:backfill", "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.ID != "job_1" || env.SessionID != "sess_1" {
		t.Errorf("body = %+v, want job_1/sess_1", env)
	}
}

func TestAdminSessionBackfillStatus_HappyPath(t *testing.T) {
	svc := &fakeAdminSvc{job: domain.BackfillJob{ID: "job_1", SessionID: "sess_1", Status: "done"}}
	h := adminRouter(svc, superAdminPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/admin/sessions/sess_1/backfill", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Status != "done" {
		t.Errorf("status = %q, want done", env.Status)
	}
}
