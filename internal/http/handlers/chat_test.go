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

// readOnlyPrincipal is a read-only api-key principal (read capability, no send) in
// the test org — used to assert send-gated chat mutations are forbidden.
func readOnlyPrincipal() *authz.Principal {
	return &authz.Principal{Kind: authz.KindAPIKey, OrganizationID: testOrganization, KeyPermissions: domain.Permissions{Read: true}}
}

// chatRouter mounts the huma chat ops behind a middleware that injects the given
// principal (nil = unauthenticated), mirroring the assertion middleware.
func chatRouter(svc ChatSvc, p *authz.Principal) http.Handler {
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
	RegisterChatOps(api, &Handlers{Chats: svc})
	return r
}

func TestListChats_HappyPath(t *testing.T) {
	svc := &fakeChatSvc{chats: store.Page[domain.Chat]{
		Items:      []domain.Chat{{ChatJID: "1@s.whatsapp.net"}, {ChatJID: "2@s.whatsapp.net"}},
		NextCursor: "42",
	}}
	h := chatRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions/sess_1/chats?limit=2", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data       []domain.Chat `json:"data"`
		NextCursor string        `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 || body.NextCursor != "42" {
		t.Errorf("unexpected body: %+v", body)
	}
	if svc.lastID != "sess_1" {
		t.Errorf("session not threaded: %q", svc.lastID)
	}
}

func TestListChats_NoPrincipal401(t *testing.T) {
	h := chatRouter(&fakeChatSvc{}, nil)
	w := doReq(h, http.MethodGet, "/api/v1/sessions/sess_1/chats", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeUnauthorized {
		t.Errorf("code = %q, want %q", got, domain.CodeUnauthorized)
	}
}

func TestGetChat_ThreadsCID(t *testing.T) {
	svc := &fakeChatSvc{one: domain.Chat{ChatJID: "1@s.whatsapp.net", Pinned: true}}
	h := chatRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions/sess_1/chats/1@s.whatsapp.net", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastCID != "1@s.whatsapp.net" {
		t.Errorf("cid not threaded: %q", svc.lastCID)
	}
}

func TestListChatMessages_Envelope(t *testing.T) {
	svc := &fakeChatSvc{messages: store.Page[domain.Message]{Items: []domain.Message{{}}, NextCursor: "7"}}
	h := chatRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions/sess_1/chats/1@s.whatsapp.net/messages", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data       []domain.Message `json:"data"`
		NextCursor string           `json:"nextCursor"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Data) != 1 || body.NextCursor != "7" {
		t.Errorf("unexpected body: %+v", body)
	}
	if svc.lastCID != "1@s.whatsapp.net" {
		t.Errorf("cid not threaded: %q", svc.lastCID)
	}
}

func TestReadChat_HappyPath(t *testing.T) {
	svc := &fakeChatSvc{one: domain.Chat{ChatJID: "1@s.whatsapp.net"}}
	h := chatRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/chats/1@s.whatsapp.net/read", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastCID != "1@s.whatsapp.net" {
		t.Errorf("cid not threaded: %q", svc.lastCID)
	}
}

func TestUpdateChat_ThreadsFlags(t *testing.T) {
	svc := &fakeChatSvc{one: domain.Chat{ChatJID: "1@s.whatsapp.net", Pinned: true}}
	h := chatRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodPatch, "/api/v1/sessions/sess_1/chats/1@s.whatsapp.net",
		`{"pinned":true,"archived":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastIn.Pinned == nil || !*svc.lastIn.Pinned {
		t.Errorf("pinned not threaded: %+v", svc.lastIn)
	}
	if svc.lastIn.Archived == nil || *svc.lastIn.Archived {
		t.Errorf("archived not threaded: %+v", svc.lastIn)
	}
}

func TestDeleteChat_NoContent(t *testing.T) {
	h := chatRouter(&fakeChatSvc{}, manageOrgPrincipal())
	w := doReq(h, http.MethodDelete, "/api/v1/sessions/sess_1/chats/1@s.whatsapp.net", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
}

func TestChatPresence_NotImplementedPropagates(t *testing.T) {
	// When the live client is unavailable the service returns not_implemented;
	// the op must surface it as 501.
	svc := &fakeChatSvc{err: domain.ErrNotImplemented("live WhatsApp client is not available for this session")}
	h := chatRouter(svc, manageOrgPrincipal())
	w := doReq(h, http.MethodPut, "/api/v1/sessions/sess_1/chats/1@s.whatsapp.net/presence",
		`{"state":"composing"}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeNotImplemented {
		t.Errorf("code = %q, want %q", got, domain.CodeNotImplemented)
	}
	if svc.lastSt != "composing" {
		t.Errorf("state not threaded: %q", svc.lastSt)
	}
}

func TestChatPresence_MissingSendCapability403(t *testing.T) {
	// A read-only api-key can read chats but must not drive send-gated mutations.
	h := chatRouter(&fakeChatSvc{}, readOnlyPrincipal())
	w := doReq(h, http.MethodPut, "/api/v1/sessions/sess_1/chats/c/presence", `{"state":"composing"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeForbidden {
		t.Errorf("code = %q, want %q", got, domain.CodeForbidden)
	}
}
