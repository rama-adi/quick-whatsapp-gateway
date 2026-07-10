package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

// Chats tests moved to chat_test.go (converted to huma — see RegisterChatOps).
// Contacts tests moved to contact_test.go (converted to huma — see RegisterContactOps).
// Groups tests moved to group_test.go (converted to huma — see RegisterGroupOps).

// ---------------------------------------------------------------------------
// Channels (converted to huma — see RegisterChannelOps)
// ---------------------------------------------------------------------------

// channelRouter mounts the huma channel ops behind a middleware that injects the
// given principal (nil = unauthenticated), mirroring the assertion middleware.
func channelRouter(svc ChannelSvc, p *authz.Principal) http.Handler {
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
	RegisterChannelOps(api, &Handlers{Channels: svc})
	return r
}

// TestCreateChannel_HappyPath verifies the valid create channel flow and its observable contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestCreateChannel_HappyPath(t *testing.T) {
	svc := &fakeChannelSvc{jid: "123@newsletter"}
	h := channelRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/channels", `{"name":"News"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "123@newsletter") {
		t.Errorf("response missing jid: %s", w.Body.String())
	}
}

// TestCreateChannel_NotImplementedPropagates verifies unsupported behavior remains an explicit 501 instead of being masked.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestCreateChannel_NotImplementedPropagates(t *testing.T) {
	svc := &fakeChannelSvc{err: domain.ErrNotImplemented("channel create is not implemented yet")}
	h := channelRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/channels", `{"name":"News"}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeNotImplemented {
		t.Errorf("code = %q, want %q", got, domain.CodeNotImplemented)
	}
}

// TestMuteChannel_DefaultsToMute verifies mute channel defaults to mute configuration and fallback behavior remain stable.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestMuteChannel_DefaultsToMute(t *testing.T) {
	svc := &fakeChannelSvc{}
	h := channelRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/channels/c@newsletter:mute", `{}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastMute == nil || !*svc.lastMute {
		t.Errorf("mute should default true: %+v", svc.lastMute)
	}
}

// TestFollowChannel_NoPrincipal401 verifies unauthenticated callers are rejected with 401 before protected work runs.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestFollowChannel_NoPrincipal401(t *testing.T) {
	h := channelRouter(&fakeChannelSvc{}, nil)
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/channels/c@newsletter:follow", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}

// TestChannel_MissingCapability403 verifies callers lacking the required authority are rejected with 403.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestChannel_MissingCapability403(t *testing.T) {
	// A read-only api-key principal must not perform send-gated channel ops.
	h := channelRouter(&fakeChannelSvc{}, readOnlyPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/channels/c@newsletter:unfollow", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Status / Presence (converted to huma — see RegisterStatusOps)
// ---------------------------------------------------------------------------

// statusRouter mounts the huma status/presence ops behind a middleware that
// injects the given principal (nil = unauthenticated), mirroring the assertion
// middleware.
func statusRouter(status StatusSvc, presence PresenceSvc, p *authz.Principal) http.Handler {
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
	RegisterStatusOps(api, &Handlers{Status: status, Presence: presence})
	return r
}

// TestPostStatus_TextHappyPath verifies the valid post status text flow and its observable contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestPostStatus_TextHappyPath(t *testing.T) {
	svc := &fakeStatusSvc{id: "WAMSG1"}
	h := statusRouter(svc, nil, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/status", `{"type":"text","text":"hi"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "WAMSG1") {
		t.Errorf("response missing messageId: %s", w.Body.String())
	}
}

// TestPostStatus_Image501 verifies unsupported behavior remains an explicit 501 instead of being masked.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestPostStatus_Image501(t *testing.T) {
	h := statusRouter(&fakeStatusSvc{}, nil, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/status", `{"type":"image"}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeNotImplemented {
		t.Errorf("code = %q, want %q", got, domain.CodeNotImplemented)
	}
}

// TestPostStatus_NoPrincipal401 verifies unauthenticated callers are rejected with 401 before protected work runs.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestPostStatus_NoPrincipal401(t *testing.T) {
	h := statusRouter(&fakeStatusSvc{}, nil, nil)
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/status", `{"type":"text","text":"hi"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}

// TestStatus_MissingCapability403 verifies callers lacking the required authority are rejected with 403.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestStatus_MissingCapability403(t *testing.T) {
	// A read-only api-key principal must not perform send-gated status ops.
	h := statusRouter(&fakeStatusSvc{}, nil, readOnlyPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/s/status", `{"type":"text","text":"hi"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

// TestSetPresence_HappyPath verifies the valid set presence flow and its observable contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestSetPresence_HappyPath(t *testing.T) {
	svc := &fakePresenceSvc{}
	h := statusRouter(nil, svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPut, "/api/v1/sessions/sess_1/presence", `{"state":"online"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastSt != "online" || svc.lastSess != "sess_1" {
		t.Errorf("state/session not threaded: %q %q", svc.lastSt, svc.lastSess)
	}
}

// TestSetPresence_ValidationPropagates verifies invalid input preserves the documented client-error mapping for set presence validation propagates.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestSetPresence_ValidationPropagates(t *testing.T) {
	svc := &fakePresenceSvc{err: domain.ErrValidation("state must be online or offline")}
	h := statusRouter(nil, svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPut, "/api/v1/sessions/s/presence", `{"state":"bogus"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}
