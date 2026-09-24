package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/humax"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

type bodyLimitMessageSvc struct {
	lastReq domain.SendRequest
}

func (f *bodyLimitMessageSvc) Send(_ context.Context, _, _ string, req domain.SendRequest, _ outbound.SendOptions) (outbound.SendResult, error) {
	f.lastReq = req
	return outbound.SendResult{Mode: outbound.ModeSync, Status: domain.MessageSent}, nil
}
func (f *bodyLimitMessageSvc) Edit(context.Context, string, string, string, string, string) (outbound.SendResult, error) {
	return outbound.SendResult{}, nil
}
func (f *bodyLimitMessageSvc) Revoke(context.Context, string, string, string, string, string) (outbound.SendResult, error) {
	return outbound.SendResult{}, nil
}
func (f *bodyLimitMessageSvc) React(context.Context, string, string, string, string, string, string) (outbound.SendResult, error) {
	return outbound.SendResult{}, nil
}
func (f *bodyLimitMessageSvc) Forward(context.Context, string, string, string, string, string, string) (outbound.SendResult, error) {
	return outbound.SendResult{}, nil
}
func (f *bodyLimitMessageSvc) Vote(context.Context, string, string, string, string, string, []string) (outbound.SendResult, error) {
	return outbound.SendResult{}, nil
}

func messageBodyRouter(svc *bodyLimitMessageSvc) (http.Handler, huma.API) {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p := &authz.Principal{Kind: authz.KindAPIKey, OrganizationID: "ten_test", KeyPermissions: domain.Permissions{Send: true}}
			next.ServeHTTP(w, req.WithContext(authz.SetPrincipal(req.Context(), p)))
		})
	})
	api := humax.NewAPI(r)
	RegisterMessageOps(api, &Handlers{Messages: svc})
	return r, api
}

// Inline media may exceed Huma's default 1 MiB JSON limit. The E2E send matrix
// uses small payloads and cannot detect this size-specific rejection.
func TestSendMessage_AcceptsInlineMediaOverDefaultBodyLimit(t *testing.T) {
	svc := &bodyLimitMessageSvc{}
	h, _ := messageBodyRouter(svc)
	data := strings.Repeat("A", (1<<20)+1)
	body := `{"type":"image","to":"628@s.whatsapp.net","media":{"data":"` + data + `","mimetype":"image/png"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || svc.lastReq.Media == nil || svc.lastReq.Media.Data != data {
		t.Fatalf("status=%d media accepted=%v", w.Code, svc.lastReq.Media != nil)
	}
}

// A synchronous send can wait on WhatsApp longer than Huma's body read timeout.
// The small, fast E2E sends cannot detect the timeout configuration regressing.
