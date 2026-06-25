package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func newWebhookHandlers(svc WebhookSvc) *Handlers { return &Handlers{Webhooks: svc} }

func TestCreateWebhook_HappyPath_SecretThreadedNotReturned(t *testing.T) {
	svc := &fakeWebhookSvc{created: domain.Webhook{ID: "wh_1", URL: "https://x", Events: []string{"message"}, HMACSecret: []byte("encrypted")}}
	h := newWebhookHandlers(svc)
	body := `{"url":"https://x","events":["message"],"secret":"shh"}`
	r := withTenant(chiReq(http.MethodPost, "/api/v1/webhooks", body, nil), testTenant)
	w := httptest.NewRecorder()
	h.CreateWebhook(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if svc.lastIn.Secret == nil || *svc.lastIn.Secret != "shh" {
		t.Errorf("secret not threaded: %+v", svc.lastIn.Secret)
	}
	// HMACSecret has json:"-": must not leak in the response.
	if contains(w.Body.String(), "encrypted") {
		t.Errorf("response leaked hmac secret: %s", w.Body.String())
	}
}

func TestCreateWebhook_NoTenant401(t *testing.T) {
	h := newWebhookHandlers(&fakeWebhookSvc{})
	r := chiReq(http.MethodPost, "/api/v1/webhooks", `{"url":"x"}`, nil)
	w := httptest.NewRecorder()
	h.CreateWebhook(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestCreateWebhook_ServiceValidation(t *testing.T) {
	svc := &fakeWebhookSvc{err: domain.ErrValidation("url is required")}
	h := newWebhookHandlers(svc)
	r := withTenant(chiReq(http.MethodPost, "/api/v1/webhooks", `{"events":["message"]}`, nil), testTenant)
	w := httptest.NewRecorder()
	h.CreateWebhook(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestListWebhooks_Envelope(t *testing.T) {
	svc := &fakeWebhookSvc{list: []domain.Webhook{{ID: "wh_1"}}}
	h := newWebhookHandlers(svc)
	r := withTenant(chiReq(http.MethodGet, "/api/v1/webhooks", "", nil), testTenant)
	w := httptest.NewRecorder()
	h.ListWebhooks(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var env struct {
		Data []domain.Webhook `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data) != 1 {
		t.Errorf("data len = %d, want 1", len(env.Data))
	}
}

func TestUpdateWebhook_HappyPath(t *testing.T) {
	svc := &fakeWebhookSvc{updated: domain.Webhook{ID: "wh_1", URL: "https://y"}}
	h := newWebhookHandlers(svc)
	r := withTenant(chiReq(http.MethodPatch, "/api/v1/webhooks/wh_1", `{"url":"https://y"}`, map[string]string{"id": "wh_1"}), testTenant)
	w := httptest.NewRecorder()
	h.UpdateWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastIn.URL != "https://y" {
		t.Errorf("url not threaded: %q", svc.lastIn.URL)
	}
}

func TestDeleteWebhook_NoContent(t *testing.T) {
	h := newWebhookHandlers(&fakeWebhookSvc{})
	r := withTenant(chiReq(http.MethodDelete, "/api/v1/webhooks/wh_1", "", map[string]string{"id": "wh_1"}), testTenant)
	w := httptest.NewRecorder()
	h.DeleteWebhook(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}

func TestGetWebhook_NotFound(t *testing.T) {
	svc := &fakeWebhookSvc{err: domain.ErrNotFound("webhook not found")}
	h := newWebhookHandlers(svc)
	r := withTenant(chiReq(http.MethodGet, "/api/v1/webhooks/x", "", map[string]string{"id": "x"}), testTenant)
	w := httptest.NewRecorder()
	h.GetWebhook(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}
