package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// contactRouter mounts the huma contact ops behind a middleware that injects the
// given principal (nil = unauthenticated), mirroring the assertion middleware.
func contactRouter(svc ContactSvc, p *authz.Principal) http.Handler {
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
	RegisterContactOps(api, &Handlers{Contacts: svc})
	return r
}

func TestListContacts_FiltersThreaded(t *testing.T) {
	svc := &fakeContactSvc{contacts: store.Page[domain.Contact]{Items: []domain.Contact{{LID: "x"}}}}
	h := contactRouter(svc, readOnlyPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions/sess_1/contacts?source=dm&group=g@g.us&q=ali", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastF.Source != "dm" || svc.lastF.GroupJID != "g@g.us" || svc.lastF.Q != "ali" {
		t.Errorf("filters not threaded: %+v", svc.lastF)
	}
}

func TestListContacts_ValidationPropagates(t *testing.T) {
	svc := &fakeContactSvc{err: domain.ErrValidation("source must be dm or group")}
	h := contactRouter(svc, readOnlyPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions/s/contacts?source=bogus", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeValidationError {
		t.Errorf("code = %q, want %q", got, domain.CodeValidationError)
	}
}

func TestListContacts_NoPrincipal401(t *testing.T) {
	h := contactRouter(&fakeContactSvc{}, nil)
	w := doReq(h, http.MethodGet, "/api/v1/sessions/s/contacts", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeUnauthorized {
		t.Errorf("code = %q, want %q", got, domain.CodeUnauthorized)
	}
}

func TestCheckContact_HappyPath(t *testing.T) {
	svc := &fakeContactSvc{check: domain.OnWhatsApp{Query: "+628", JID: "628@s.whatsapp.net", IsIn: true}}
	h := contactRouter(svc, readOnlyPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions/s/contacts/check?phone=%2B628", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got domain.OnWhatsApp
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.IsIn {
		t.Errorf("expected isOnWhatsApp true: %+v", got)
	}
}

func TestGetContactAbout_HappyPath(t *testing.T) {
	svc := &fakeContactSvc{about: "hey there"}
	h := contactRouter(svc, readOnlyPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions/s/contacts/628@s.whatsapp.net/about", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got struct {
		About string `json:"about"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.About != "hey there" {
		t.Errorf("about = %q, want %q", got.About, "hey there")
	}
}

func TestBlockContact_ThreadsTrue(t *testing.T) {
	svc := &fakeContactSvc{}
	h := contactRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/contacts/628@s.whatsapp.net/block", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.blocked == nil || !*svc.blocked {
		t.Errorf("block flag not threaded: %+v", svc.blocked)
	}
	if svc.lastJID != "628@s.whatsapp.net" {
		t.Errorf("jid not threaded/decoded: %q", svc.lastJID)
	}
}

func TestUnblockContact_ThreadsFalse(t *testing.T) {
	svc := &fakeContactSvc{}
	h := contactRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/contacts/628@s.whatsapp.net/unblock", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.blocked == nil || *svc.blocked {
		t.Errorf("unblock flag not threaded: %+v", svc.blocked)
	}
}

func TestBlockContact_MissingCapability403(t *testing.T) {
	// A read-only api-key principal must not block contacts (send-gated).
	h := contactRouter(&fakeContactSvc{}, readOnlyPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/contacts/628@s.whatsapp.net/block", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}
