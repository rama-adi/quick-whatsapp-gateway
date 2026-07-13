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
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// sendOrgPrincipal is an api-key principal with the send capability in the test org.
func sendOrgPrincipal() *authz.Principal {
	return &authz.Principal{Kind: authz.KindAPIKey, OrganizationID: testOrganization, KeyPermissions: domain.Permissions{Send: true}}
}

// messageRouter builds a chi router with the huma message ops mounted behind a
// middleware that injects the given principal (nil = unauthenticated).
func messageRouter(svc MessageSvc, p *authz.Principal) http.Handler {
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
	RegisterMessageOps(api, &Handlers{Messages: svc})
	return r
}

// TestSendMessage_SyncHappyPath verifies the valid send message sync flow and its observable contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestSendMessage_SyncHappyPath(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync, WAMessageID: "WA1", Status: domain.MessageSent}}
	h := messageRouter(svc, sendOrgPrincipal())
	body := `{"type":"text","to":"628@s.whatsapp.net","text":"hi"}`
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s1/messages", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastReq.Type != domain.SendTypeText || svc.lastReq.Text != "hi" {
		t.Errorf("request not threaded: %+v", svc.lastReq)
	}
	var got outbound.SendResult
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.WAMessageID != "WA1" {
		t.Errorf("waMessageId = %q, want WA1", got.WAMessageID)
	}
}

// TestSendMessage_AcceptsInlineMediaOverDefaultBodyLimit protects the send
// endpoint's media-specific allowance from regressing to Huma's 1 MiB default.
func TestSendMessage_AcceptsInlineMediaOverDefaultBodyLimit(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync, Status: domain.MessageSent}}
	h := messageRouter(svc, sendOrgPrincipal())
	data := strings.Repeat("A", (1<<20)+1)
	body := `{"type":"image","to":"628@s.whatsapp.net","media":{"data":"` + data + `","mimetype":"image/png"}}`

	w := doReq(h, http.MethodPost, "/api/v1/sessions/s1/messages", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastReq.Media == nil || svc.lastReq.Media.Data != data {
		t.Fatal("inline media was not forwarded to the message service")
	}
}

// TestSendMessage_AsyncIs202_AndOptionsThreaded verifies the send message async is202 and options threaded behavior remains part of the package contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestSendMessage_AsyncIs202_AndOptionsThreaded(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeAsync, OutboxID: "out_1"}}
	h := messageRouter(svc, sendOrgPrincipal())
	body := `{"type":"text","to":"628@s.whatsapp.net","text":"hi"}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s1/messages?async", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "key-1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if !svc.lastOpts.Async {
		t.Error("Async option not set")
	}
	if svc.lastOpts.IdempotencyKey != "key-1" {
		t.Errorf("idempotency key = %q, want key-1", svc.lastOpts.IdempotencyKey)
	}
}

// TestSendMessage_NoPrincipal401 verifies unauthenticated callers are rejected with 401 before protected work runs.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestSendMessage_NoPrincipal401(t *testing.T) {
	h := messageRouter(&fakeMessageSvc{}, nil)
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s1/messages", `{"type":"text"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeUnauthorized {
		t.Errorf("code = %q, want %q", got, domain.CodeUnauthorized)
	}
}

// TestSendMessage_ServiceValidationError verifies invalid input preserves the documented client-error mapping for send message service validation error.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestSendMessage_ServiceValidationError(t *testing.T) {
	svc := &fakeMessageSvc{err: domain.ErrValidation("text is required")}
	h := messageRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s1/messages", `{"type":"text","to":"x"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeValidationError {
		t.Errorf("code = %q, want %q", got, domain.CodeValidationError)
	}
}

// TestSendMessage_RateLimited429 verifies rate-limit denial preserves the public 429 response contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestSendMessage_RateLimited429(t *testing.T) {
	svc := &fakeMessageSvc{err: domain.ErrRateLimited("slow down")}
	h := messageRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s1/messages", `{"type":"text","to":"x","text":"y"}`)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", w.Code, w.Body.String())
	}
}

// TestSendMessage_MissingCapability403 verifies callers lacking the required authority are rejected with 403.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestSendMessage_MissingCapability403(t *testing.T) {
	// A read-only api-key principal must not send messages.
	p := &authz.Principal{Kind: authz.KindAPIKey, OrganizationID: testOrganization, KeyPermissions: domain.Permissions{Read: true}}
	h := messageRouter(&fakeMessageSvc{}, p)
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s1/messages", `{"type":"text","to":"x","text":"y"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

// TestEditMessage_Routes verifies adapter routing forwards the required edit message routes inputs without loss.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestEditMessage_Routes(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync}}
	h := messageRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPatch, "/api/v1/sessions/s1/messages/m1", `{"chat":"c1","text":"new"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "edit" {
		t.Errorf("op = %q, want edit", svc.lastOp)
	}
}

// TestRevokeMessage_Routes verifies adapter routing forwards the required revoke message routes inputs without loss.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestRevokeMessage_Routes(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync}}
	h := messageRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodDelete, "/api/v1/sessions/s1/messages/m1", `{"chat":"c1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "revoke" {
		t.Errorf("op = %q, want revoke", svc.lastOp)
	}
}

// TestAddReaction_PassesEmoji verifies adapter routing forwards the required add reaction passes emoji inputs without loss.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestAddReaction_PassesEmoji(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync}}
	h := messageRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s1/messages/m1/reaction", `{"chat":"c1","emoji":"👍"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "react:👍" {
		t.Errorf("op = %q, want react:👍", svc.lastOp)
	}
}

// TestRemoveReaction_ClearsEmoji verifies the remove reaction clears emoji behavior remains part of the package contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestRemoveReaction_ClearsEmoji(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync}}
	h := messageRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodDelete, "/api/v1/sessions/s1/messages/m1/reaction", `{"chat":"c1","emoji":"👍"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "react:" {
		t.Errorf("op = %q, want react: (empty emoji)", svc.lastOp)
	}
}

// TestForwardMessage_Routes verifies adapter routing forwards the required forward message routes inputs without loss.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestForwardMessage_Routes(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync}}
	h := messageRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s1/messages/m1/forward", `{"chat":"c1","to":"628@s.whatsapp.net"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "forward" {
		t.Errorf("op = %q, want forward", svc.lastOp)
	}
}

// TestVoteMessage_Routes verifies adapter routing forwards the required vote message routes inputs without loss.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestVoteMessage_Routes(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync}}
	h := messageRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s1/messages/m1/vote", `{"chat":"c1","options":["A"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "vote" {
		t.Errorf("op = %q, want vote", svc.lastOp)
	}
}
