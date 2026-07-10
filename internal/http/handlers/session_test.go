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

// sessionRouter builds a chi router with the huma session ops mounted behind a
// middleware that injects the given principal (nil = unauthenticated).
func sessionRouter(svc SessionSvc, p *authz.Principal) http.Handler {
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
	RegisterSessionOps(api, &Handlers{Sessions: svc})
	return r
}

// TestCreateSession_HappyPath verifies the valid create session flow and its observable contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestCreateSession_HappyPath(t *testing.T) {
	svc := &fakeSessionSvc{created: domain.WASession{ID: "sess_1", OrganizationID: testOrganization, Status: domain.SessionStopped}}
	h := sessionRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions", `{"label":"work","autoRead":false}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var got domain.WASession
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "sess_1" {
		t.Errorf("id = %q, want sess_1", got.ID)
	}
	if svc.createIn.AutoRead == nil || *svc.createIn.AutoRead != false {
		t.Errorf("autoRead not threaded to service: %+v", svc.createIn)
	}
}

// TestCreateSession_NoPrincipal401 verifies unauthenticated callers are rejected with 401 before protected work runs.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestCreateSession_NoPrincipal401(t *testing.T) {
	h := sessionRouter(&fakeSessionSvc{}, nil)
	w := doReq(h, http.MethodPost, "/api/v1/sessions", `{}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeUnauthorized {
		t.Errorf("code = %q, want %q", got, domain.CodeUnauthorized)
	}
}

// TestCreateSession_ServiceValidation verifies invalid input preserves the documented client-error mapping for create session service validation.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestCreateSession_ServiceValidation(t *testing.T) {
	svc := &fakeSessionSvc{err: domain.ErrValidation("label too long")}
	h := sessionRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions", `{"label":"x"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeValidationError {
		t.Errorf("code = %q, want %q", got, domain.CodeValidationError)
	}
}

// TestListSessions_Envelope verifies list sessions responses retain the documented envelope shape.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestListSessions_Envelope(t *testing.T) {
	svc := &fakeSessionSvc{list: []domain.WASession{{ID: "sess_1"}, {ID: "sess_2"}}}
	h := sessionRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data []domain.WASession `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 2 {
		t.Errorf("data len = %d, want 2", len(env.Data))
	}
}

// TestStartSession_ReturnsRefreshedRow verifies the start session returns refreshed row behavior remains part of the package contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestStartSession_ReturnsRefreshedRow(t *testing.T) {
	svc := &fakeSessionSvc{one: domain.WASession{ID: "sess_1", Status: domain.SessionWorking}}
	h := sessionRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1:start", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastID != "sess_1" {
		t.Errorf("service saw id %q, want sess_1", svc.lastID)
	}
}

// TestStopSession_RoutesColonAction verifies adapter routing forwards the required stop session routes colon action inputs without loss.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestStopSession_RoutesColonAction(t *testing.T) {
	svc := &fakeSessionSvc{one: domain.WASession{ID: "sess_1", Status: domain.SessionStopped}}
	h := sessionRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1:stop", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastID != "sess_1" {
		t.Errorf("service saw id %q, want sess_1", svc.lastID)
	}
}

// TestDeleteSession_NoContent verifies delete session no content succeeds with an empty 204 response.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestDeleteSession_NoContent(t *testing.T) {
	svc := &fakeSessionSvc{}
	h := sessionRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodDelete, "/api/v1/sessions/sess_1", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastID != "sess_1" {
		t.Errorf("service saw id %q, want sess_1", svc.lastID)
	}
}

// TestGetSession_NotFound verifies invalid input preserves the documented client-error mapping for get session not found.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestGetSession_NotFound(t *testing.T) {
	svc := &fakeSessionSvc{err: domain.ErrNotFound("session not found")}
	h := sessionRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions/x", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// TestSessionPairingCode_HappyPath verifies the valid session pairing code flow and its observable contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestSessionPairingCode_HappyPath(t *testing.T) {
	svc := &fakeSessionSvc{code: "ABCD-1234"}
	h := sessionRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/pairing-code", `{"phone":"62812345"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastPhone != "62812345" {
		t.Errorf("phone = %q, want 62812345", svc.lastPhone)
	}
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "ABCD-1234" {
		t.Errorf("code = %q, want ABCD-1234", got["code"])
	}
}

// TestSession_MissingCapability403 verifies callers lacking the required authority are rejected with 403.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestSession_MissingCapability403(t *testing.T) {
	// A read-only api-key principal must not manage sessions.
	p := &authz.Principal{Kind: authz.KindAPIKey, OrganizationID: testOrganization, KeyPermissions: domain.Permissions{Read: true}}
	h := sessionRouter(&fakeSessionSvc{}, p)
	w := doReq(h, http.MethodGet, "/api/v1/sessions", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}
