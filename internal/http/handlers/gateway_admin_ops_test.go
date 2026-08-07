package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service/gatewayadmin"
)

type fakeGatewayAdminSvc struct {
	list      []domain.Gateway
	detail    domain.GatewayAdminDetail
	issued    gatewayadmin.IssuedEnrollment
	err       error
	called    string
	actor     gatewayadmin.Actor
	deleteAck bool
}

func (f *fakeGatewayAdminSvc) ListGateways(context.Context) ([]domain.Gateway, error) {
	f.called = "list"
	return f.list, f.err
}
func (f *fakeGatewayAdminSvc) GetGateway(context.Context, string) (domain.GatewayAdminDetail, error) {
	f.called = "get"
	return f.detail, f.err
}
func (f *fakeGatewayAdminSvc) CreateGateway(_ context.Context, in gatewayadmin.CreateGatewayInput) (gatewayadmin.IssuedEnrollment, error) {
	f.called, f.actor = "create", in.Actor
	return f.issued, f.err
}
func (f *fakeGatewayAdminSvc) ReplaceEnrollmentToken(_ context.Context, _ string, actor gatewayadmin.Actor) (gatewayadmin.IssuedEnrollment, error) {
	f.called, f.actor = "replace-token", actor
	return f.issued, f.err
}
func (f *fakeGatewayAdminSvc) Drain(_ context.Context, _ string, actor gatewayadmin.Actor) error {
	f.called, f.actor = "drain", actor
	return f.err
}
func (f *fakeGatewayAdminSvc) Resume(_ context.Context, _ string, actor gatewayadmin.Actor) error {
	f.called, f.actor = "resume", actor
	return f.err
}
func (f *fakeGatewayAdminSvc) Disable(_ context.Context, _ string, actor gatewayadmin.Actor) error {
	f.called, f.actor = "disable", actor
	return f.err
}
func (f *fakeGatewayAdminSvc) Reenable(_ context.Context, _ string, actor gatewayadmin.Actor) error {
	f.called, f.actor = "reenable", actor
	return f.err
}
func (f *fakeGatewayAdminSvc) Reenroll(_ context.Context, _ string, actor gatewayadmin.Actor) (gatewayadmin.IssuedEnrollment, error) {
	f.called, f.actor = "reenroll", actor
	return f.issued, f.err
}
func (f *fakeGatewayAdminSvc) Delete(_ context.Context, _ string, ack bool, actor gatewayadmin.Actor) error {
	f.called, f.actor, f.deleteAck = "delete", actor, ack
	return f.err
}

func gatewayAdminHandler(svc GatewayAdminSvc, p *authz.Principal) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(authz.SetPrincipal(r.Context(), p)))
		})
	})
	api := humax.NewAPI(r)
	RegisterGatewayAdminOps(api, &Handlers{GatewayAdmin: svc})
	return r
}

func TestGatewayAdminOpsRequireSuperAdminBeforeService(t *testing.T) {
	svc := &fakeGatewayAdminSvc{}
	h := gatewayAdminHandler(svc, &authz.Principal{Kind: authz.KindUser, UserID: "user_1", PlatformRole: "user"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/gateways", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
	}
	if svc.called != "" {
		t.Fatalf("service called %q before authorization", svc.called)
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeForbidden {
		t.Fatalf("error code = %q, want forbidden", got)
	}
}

func TestGatewayAdminOpsCreateReturnsTokenOnceAndActor(t *testing.T) {
	svc := &fakeGatewayAdminSvc{issued: gatewayadmin.IssuedEnrollment{GatewayID: "gw_1", TokenID: "ent_1", Token: "qwg_enroll_v1_secret", ExpiresAt: 123}}
	h := gatewayAdminHandler(svc, &authz.Principal{Kind: authz.KindUser, UserID: "admin_1", PlatformRole: authz.PlatformRoleSuperAdmin})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/gateways", strings.NewReader(`{"label":"edge"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Request-Id", "req_1")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	if svc.called != "create" || svc.actor.UserID != "admin_1" {
		t.Fatalf("service call = %q actor = %#v", svc.called, svc.actor)
	}
	var got struct {
		Token     string `json:"token"`
		GatewayID string `json:"gatewayId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Token != "qwg_enroll_v1_secret" || got.GatewayID != "gw_1" {
		t.Fatalf("response = %#v", got)
	}
}

func TestGatewayAdminOpsMapsStateConflictAndAcknowledgesSafeDelete(t *testing.T) {
	svc := &fakeGatewayAdminSvc{err: &gatewayadmin.StateConflictError{Cause: errors.New("unsafe")}}
	h := gatewayAdminHandler(svc, &authz.Principal{Kind: authz.KindUser, UserID: "admin_1", PlatformRole: authz.PlatformRoleSuperAdmin})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/gateways/gw_1:drain", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeConflict {
		t.Fatalf("error code = %q, want conflict", got)
	}

	svc.err = nil
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/gateways/gw_1", strings.NewReader(`{"consequencesAcknowledged":true}`))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", w.Code, w.Body.String())
	}
	if svc.called != "delete" || !svc.deleteAck {
		t.Fatalf("delete call = %q acknowledged = %v", svc.called, svc.deleteAck)
	}
}
