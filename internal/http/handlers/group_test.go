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

// groupRouter mounts the huma group ops behind a middleware that injects the given
// principal (nil = unauthenticated), mirroring the assertion middleware.
func groupRouter(svc GroupSvc, p *authz.Principal) http.Handler {
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
	RegisterGroupOps(api, &Handlers{Groups: svc})
	return r
}

// TestCreateGroup_HappyPath verifies the valid create group flow and its observable contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestCreateGroup_HappyPath(t *testing.T) {
	svc := &fakeGroupSvc{info: domain.GroupInfo{GroupJID: "120@g.us"}}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/groups", `{"name":"team","participants":["1@s.whatsapp.net"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "create" {
		t.Errorf("lastOp = %q, want create", svc.lastOp)
	}
}

// TestCreateGroup_ServiceValidation verifies invalid input preserves the documented client-error mapping for create group service validation.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestCreateGroup_ServiceValidation(t *testing.T) {
	svc := &fakeGroupSvc{err: domain.ErrValidation("name is required")}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/groups", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeValidationError {
		t.Errorf("code = %q, want %q", got, domain.CodeValidationError)
	}
}

// TestListGroups_Envelope verifies list groups responses retain the documented envelope shape.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestListGroups_Envelope(t *testing.T) {
	svc := &fakeGroupSvc{groups: []domain.Group{{GroupJID: "120@g.us"}}}
	h := groupRouter(svc, readOnlyPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions/sess_1/groups", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data []domain.Group `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data) != 1 || env.Data[0].GroupJID != "120@g.us" {
		t.Errorf("data = %+v, want one 120@g.us", env.Data)
	}
}

// TestGetGroup_NoPrincipal401 verifies unauthenticated callers are rejected with 401 before protected work runs.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestGetGroup_NoPrincipal401(t *testing.T) {
	h := groupRouter(&fakeGroupSvc{}, nil)
	w := doReq(h, http.MethodGet, "/api/v1/sessions/sess_1/groups/120@g.us", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeUnauthorized {
		t.Errorf("code = %q, want %q", got, domain.CodeUnauthorized)
	}
}

// TestListGroupMembers_Envelope verifies list group members responses retain the documented envelope shape.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestListGroupMembers_Envelope(t *testing.T) {
	svc := &fakeGroupSvc{members: []domain.GroupMember{{LID: "1@s.whatsapp.net"}}}
	h := groupRouter(svc, readOnlyPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions/sess_1/groups/120@g.us/members", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data []domain.GroupMember `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data) != 1 {
		t.Errorf("data = %+v, want one member", env.Data)
	}
}

// TestAddGroupMembers_204 verifies add group members 204 succeeds with an empty 204 response.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestAddGroupMembers_204(t *testing.T) {
	svc := &fakeGroupSvc{}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/groups/120@g.us/members", `{"participants":["1@s.whatsapp.net"]}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "add" {
		t.Errorf("lastOp = %q, want add", svc.lastOp)
	}
}

// TestRemoveGroupMember_204 verifies remove group member 204 succeeds with an empty 204 response.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestRemoveGroupMember_204(t *testing.T) {
	svc := &fakeGroupSvc{}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodDelete, "/api/v1/sessions/sess_1/groups/120@g.us/members/1@s.whatsapp.net", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "remove" {
		t.Errorf("lastOp = %q, want remove", svc.lastOp)
	}
}

// TestPromoteGroupMember_204 verifies promote group member 204 succeeds with an empty 204 response.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestPromoteGroupMember_204(t *testing.T) {
	svc := &fakeGroupSvc{}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/groups/120@g.us/members/1@s.whatsapp.net/promote", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "promote" {
		t.Errorf("lastOp = %q, want promote", svc.lastOp)
	}
}

// TestDemoteGroupMember_204 verifies demote group member 204 succeeds with an empty 204 response.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestDemoteGroupMember_204(t *testing.T) {
	svc := &fakeGroupSvc{}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/groups/120@g.us/members/1@s.whatsapp.net/demote", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "demote" {
		t.Errorf("lastOp = %q, want demote", svc.lastOp)
	}
}

// TestUpdateGroup_204_ThreadsSettings verifies adapter routing forwards the required update group 204 threads settings inputs without loss.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestUpdateGroup_204_ThreadsSettings(t *testing.T) {
	svc := &fakeGroupSvc{}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPatch, "/api/v1/sessions/sess_1/groups/120@g.us", `{"subject":"new"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastIn.Subject == nil || *svc.lastIn.Subject != "new" {
		t.Errorf("subject not threaded: %+v", svc.lastIn.Subject)
	}
}

// TestGetGroupInvite_Read verifies the get group invite read behavior remains part of the package contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestGetGroupInvite_Read(t *testing.T) {
	svc := &fakeGroupSvc{invite: "https://chat.whatsapp.com/abc"}
	h := groupRouter(svc, readOnlyPrincipal())
	w := doReq(h, http.MethodGet, "/api/v1/sessions/sess_1/groups/120@g.us/invite", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Invite string `json:"invite"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Invite != "https://chat.whatsapp.com/abc" {
		t.Errorf("invite = %q, want the link", env.Invite)
	}
}

// TestRevokeGroupInvite_Send verifies the revoke group invite send behavior remains part of the package contract.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestRevokeGroupInvite_Send(t *testing.T) {
	svc := &fakeGroupSvc{invite: "https://chat.whatsapp.com/new"}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodDelete, "/api/v1/sessions/sess_1/groups/120@g.us/invite", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Invite string `json:"invite"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Invite != "https://chat.whatsapp.com/new" {
		t.Errorf("invite = %q, want the new link", env.Invite)
	}
}

// TestJoinGroup_ColonRoute verifies adapter routing forwards the required join group colon route inputs without loss.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestJoinGroup_ColonRoute(t *testing.T) {
	svc := &fakeGroupSvc{joinJID: "120@g.us"}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/groups:join", `{"invite":"https://chat.whatsapp.com/abc"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "join" {
		t.Errorf("lastOp = %q, want join", svc.lastOp)
	}
	var env struct {
		GroupJID string `json:"groupJid"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.GroupJID != "120@g.us" {
		t.Errorf("groupJid = %q, want 120@g.us", env.GroupJID)
	}
}

// TestLeaveGroup_ColonRoute_204 verifies adapter routing forwards the required leave group colon route 204 inputs without loss.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestLeaveGroup_ColonRoute_204(t *testing.T) {
	svc := &fakeGroupSvc{}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/groups/120@g.us:leave", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "leave" {
		t.Errorf("lastOp = %q, want leave", svc.lastOp)
	}
}

// TestApproveGroupMembers_ColonRoute_204 verifies adapter routing forwards the required approve group members colon route 204 inputs without loss.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestApproveGroupMembers_ColonRoute_204(t *testing.T) {
	svc := &fakeGroupSvc{}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/groups/120@g.us/members:approve", `{"participants":["1@s.whatsapp.net"]}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "approve" {
		t.Errorf("lastOp = %q, want approve", svc.lastOp)
	}
}

// TestApproveGroupMembers_NotImplementedPropagates verifies unsupported behavior remains an explicit 501 instead of being masked.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestApproveGroupMembers_NotImplementedPropagates(t *testing.T) {
	svc := &fakeGroupSvc{err: domain.ErrNotImplemented("group membership approval is not implemented yet")}
	h := groupRouter(svc, sendOrgPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/groups/120@g.us/members:approve", `{"participants":["a@s.whatsapp.net"]}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeNotImplemented {
		t.Errorf("code = %q, want %q", got, domain.CodeNotImplemented)
	}
}

// TestCreateGroup_MissingCapability403 verifies callers lacking the required authority are rejected with 403.
// It drives the registered HTTP surface with controlled service doubles and checks the response or forwarded arguments.
// This catches adapter regressions that could alter authorization, routing, or the documented wire contract.
func TestCreateGroup_MissingCapability403(t *testing.T) {
	// A read-only api-key principal must not create groups (send-gated).
	h := groupRouter(&fakeGroupSvc{}, readOnlyPrincipal())
	w := doReq(h, http.MethodPost, "/api/v1/sessions/sess_1/groups", `{"name":"x"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}
