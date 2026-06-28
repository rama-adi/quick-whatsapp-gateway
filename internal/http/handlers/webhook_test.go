package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

// manageOrgPrincipal is a JWT owner principal (manage capability) in the test org.
func manageOrgPrincipal() *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, OrganizationID: testOrganization, OrgRole: authz.OrgRoleOwner}
}

// webhookRouter builds a chi router with the huma webhook ops mounted behind a
// middleware that injects the given principal (nil = unauthenticated), mirroring
// how the assertion middleware populates the principal in production.
func webhookRouter(svc WebhookSvc, p *authz.Principal) http.Handler {
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
	RegisterWebhookOps(api, &Handlers{Webhooks: svc})
	return r
}

func doReq(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCreateWebhook_HappyPath_SecretThreadedNotReturned(t *testing.T) {
	svc := &fakeWebhookSvc{created: domain.Webhook{ID: "wh_1", URL: "https://x", Events: []string{"message"}, HMACSecret: []byte("encrypted")}}
	h := webhookRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/webhooks", `{"url":"https://x","events":["message"],"secret":"shh"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if svc.lastIn.Secret == nil || *svc.lastIn.Secret != "shh" {
		t.Errorf("secret not threaded: %+v", svc.lastIn.Secret)
	}
	if strings.Contains(w.Body.String(), "encrypted") {
		t.Errorf("response leaked hmac secret: %s", w.Body.String())
	}
}

func TestCreateWebhook_NoPrincipal401(t *testing.T) {
	h := webhookRouter(&fakeWebhookSvc{}, nil)
	w := doReq(h, http.MethodPost, "/api/v1/webhooks", `{"url":"x"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeUnauthorized {
		t.Errorf("code = %q, want %q", got, domain.CodeUnauthorized)
	}
}

func TestCreateWebhook_ServiceValidation(t *testing.T) {
	svc := &fakeWebhookSvc{err: domain.ErrValidation("url is required")}
	h := webhookRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/webhooks", `{"events":["message"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeValidationError {
		t.Errorf("code = %q, want %q", got, domain.CodeValidationError)
	}
}

func TestListWebhooks_Envelope(t *testing.T) {
	svc := &fakeWebhookSvc{list: []domain.Webhook{{ID: "wh_1"}}}
	h := webhookRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/webhooks", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data []domain.Webhook `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data) != 1 || env.Data[0].ID != "wh_1" {
		t.Errorf("data = %+v, want one wh_1", env.Data)
	}
}

func TestUpdateWebhook_HappyPath(t *testing.T) {
	svc := &fakeWebhookSvc{updated: domain.Webhook{ID: "wh_1", URL: "https://y"}}
	h := webhookRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodPatch, "/api/v1/webhooks/wh_1", `{"url":"https://y"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastIn.URL != "https://y" {
		t.Errorf("url not threaded: %q", svc.lastIn.URL)
	}
}

func TestDeleteWebhook_204(t *testing.T) {
	h := webhookRouter(&fakeWebhookSvc{}, manageOrgPrincipal())
	w := doReq(h, http.MethodDelete, "/api/v1/webhooks/wh_1", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
}

func TestWebhook_MissingCapability403(t *testing.T) {
	// A read-only api-key principal must not manage webhooks.
	p := &authz.Principal{Kind: authz.KindAPIKey, OrganizationID: testOrganization, KeyPermissions: domain.Permissions{Read: true}}
	h := webhookRouter(&fakeWebhookSvc{}, p)
	w := doReq(h, http.MethodGet, "/api/v1/webhooks", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}
