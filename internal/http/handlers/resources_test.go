package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// ---------------------------------------------------------------------------
// Chats
// ---------------------------------------------------------------------------

func TestListChats_HappyPath(t *testing.T) {
	svc := &fakeChatSvc{chats: store.Page[domain.Chat]{
		Items:      []domain.Chat{{ChatJID: "1@s.whatsapp.net"}, {ChatJID: "2@s.whatsapp.net"}},
		NextCursor: "42",
	}}
	h := &Handlers{Chats: svc}
	r := withTenant(chiReq(http.MethodGet, "/x?limit=2", "", map[string]string{"session": "sess_1"}), testTenant)
	w := httptest.NewRecorder()
	h.ListChats(w, r)
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
}

func TestListChats_NoTenant401(t *testing.T) {
	h := &Handlers{Chats: &fakeChatSvc{}}
	r := chiReq(http.MethodGet, "/x", "", map[string]string{"session": "sess_1"})
	w := httptest.NewRecorder()
	h.ListChats(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestUpdateChat_ThreadsFlags(t *testing.T) {
	svc := &fakeChatSvc{one: domain.Chat{ChatJID: "1@s.whatsapp.net", Pinned: true}}
	h := &Handlers{Chats: svc}
	r := withTenant(chiReq(http.MethodPatch, "/x", `{"pinned":true,"archived":false}`,
		map[string]string{"session": "sess_1", "cid": "1@s.whatsapp.net"}), testTenant)
	w := httptest.NewRecorder()
	h.UpdateChat(w, r)
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

func TestUpdateChat_BadJSON400(t *testing.T) {
	h := &Handlers{Chats: &fakeChatSvc{}}
	r := withTenant(chiReq(http.MethodPatch, "/x", `{"pinned":`, map[string]string{"session": "s", "cid": "c"}), testTenant)
	w := httptest.NewRecorder()
	h.UpdateChat(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDeleteChat_NoContent(t *testing.T) {
	h := &Handlers{Chats: &fakeChatSvc{}}
	r := withTenant(chiReq(http.MethodDelete, "/x", "", map[string]string{"session": "s", "cid": "c"}), testTenant)
	w := httptest.NewRecorder()
	h.DeleteChat(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}

func TestChatPresence_NotImplementedPropagates(t *testing.T) {
	// When the live client is unavailable the service returns not_implemented;
	// the handler must surface it as 501.
	svc := &fakeChatSvc{err: domain.ErrNotImplemented("live WhatsApp client is not available for this session")}
	h := &Handlers{Chats: svc}
	r := withTenant(chiReq(http.MethodPut, "/x", `{"state":"composing"}`,
		map[string]string{"session": "s", "cid": "c"}), testTenant)
	w := httptest.NewRecorder()
	h.ChatPresence(w, r)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Contacts
// ---------------------------------------------------------------------------

func TestListContacts_FiltersThreaded(t *testing.T) {
	svc := &fakeContactSvc{contacts: store.Page[domain.Contact]{Items: []domain.Contact{{LID: "x"}}}}
	h := &Handlers{Contacts: svc}
	r := withTenant(chiReq(http.MethodGet, "/x?source=dm&group=g@g.us&q=ali", "",
		map[string]string{"session": "sess_1"}), testTenant)
	w := httptest.NewRecorder()
	h.ListContacts(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastF.Source != "dm" || svc.lastF.GroupJID != "g@g.us" || svc.lastF.Q != "ali" {
		t.Errorf("filters not threaded: %+v", svc.lastF)
	}
}

func TestListContacts_ValidationPropagates(t *testing.T) {
	svc := &fakeContactSvc{err: domain.ErrValidation("source must be dm or group")}
	h := &Handlers{Contacts: svc}
	r := withTenant(chiReq(http.MethodGet, "/x?source=bogus", "", map[string]string{"session": "s"}), testTenant)
	w := httptest.NewRecorder()
	h.ListContacts(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestCheckContact_HappyPath(t *testing.T) {
	svc := &fakeContactSvc{check: domain.OnWhatsApp{Query: "+628", JID: "628@s.whatsapp.net", IsIn: true}}
	h := &Handlers{Contacts: svc}
	r := withTenant(chiReq(http.MethodGet, "/x?phone=%2B628", "", map[string]string{"session": "s"}), testTenant)
	w := httptest.NewRecorder()
	h.CheckContact(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got domain.OnWhatsApp
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.IsIn {
		t.Errorf("expected isOnWhatsApp true: %+v", got)
	}
}

func TestBlockContact_ThreadsTrue(t *testing.T) {
	svc := &fakeContactSvc{}
	h := &Handlers{Contacts: svc}
	r := withTenant(chiReq(http.MethodPost, "/x", "", map[string]string{"session": "s", "jid": "628@s.whatsapp.net"}), testTenant)
	w := httptest.NewRecorder()
	h.BlockContact(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if svc.blocked == nil || !*svc.blocked {
		t.Errorf("block flag not threaded: %+v", svc.blocked)
	}
}

func TestUnblockContact_ThreadsFalse(t *testing.T) {
	svc := &fakeContactSvc{}
	h := &Handlers{Contacts: svc}
	r := withTenant(chiReq(http.MethodPost, "/x", "", map[string]string{"session": "s", "jid": "628@s.whatsapp.net"}), testTenant)
	w := httptest.NewRecorder()
	h.UnblockContact(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if svc.blocked == nil || *svc.blocked {
		t.Errorf("unblock flag not threaded: %+v", svc.blocked)
	}
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

func TestCreateGroup_HappyPath(t *testing.T) {
	svc := &fakeGroupSvc{info: domain.GroupInfo{GroupJID: "123@g.us", Subject: "Team"}}
	h := &Handlers{Groups: svc}
	r := withTenant(chiReq(http.MethodPost, "/x", `{"name":"Team","participants":["628@s.whatsapp.net"]}`,
		map[string]string{"session": "s"}), testTenant)
	w := httptest.NewRecorder()
	h.CreateGroup(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "create" {
		t.Errorf("op = %q, want create", svc.lastOp)
	}
}

func TestCreateGroup_ValidationPropagates(t *testing.T) {
	svc := &fakeGroupSvc{err: domain.ErrValidation("name is required")}
	h := &Handlers{Groups: svc}
	r := withTenant(chiReq(http.MethodPost, "/x", `{}`, map[string]string{"session": "s"}), testTenant)
	w := httptest.NewRecorder()
	h.CreateGroup(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestCreateGroup_NoTenant401(t *testing.T) {
	h := &Handlers{Groups: &fakeGroupSvc{}}
	r := chiReq(http.MethodPost, "/x", `{"name":"t"}`, map[string]string{"session": "s"})
	w := httptest.NewRecorder()
	h.CreateGroup(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestGroupParticipantActions(t *testing.T) {
	cases := []struct {
		name   string
		fn     func(h *Handlers, w http.ResponseWriter, r *http.Request)
		body   string
		wantOp string
	}{
		{"add", func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.AddGroupMembers(w, r) }, `{"participants":["a@s.whatsapp.net"]}`, "add"},
		{"remove", func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.RemoveGroupMember(w, r) }, "", "remove"},
		{"promote", func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.PromoteGroupMember(w, r) }, "", "promote"},
		{"demote", func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.DemoteGroupMember(w, r) }, "", "demote"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &fakeGroupSvc{}
			h := &Handlers{Groups: svc}
			r := withTenant(chiReq(http.MethodPost, "/x", c.body,
				map[string]string{"session": "s", "gid": "g@g.us", "jid": "a@s.whatsapp.net"}), testTenant)
			w := httptest.NewRecorder()
			c.fn(h, w, r)
			if w.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
			}
			if svc.lastOp != c.wantOp {
				t.Errorf("op = %q, want %q", svc.lastOp, c.wantOp)
			}
		})
	}
}

func TestUpdateGroup_ThreadsSettings(t *testing.T) {
	svc := &fakeGroupSvc{}
	h := &Handlers{Groups: svc}
	r := withTenant(chiReq(http.MethodPatch, "/x", `{"subject":"New","announce":true}`,
		map[string]string{"session": "s", "gid": "g@g.us"}), testTenant)
	w := httptest.NewRecorder()
	h.UpdateGroup(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastIn.Subject == nil || *svc.lastIn.Subject != "New" {
		t.Errorf("subject not threaded: %+v", svc.lastIn)
	}
	if svc.lastIn.Announce == nil || !*svc.lastIn.Announce {
		t.Errorf("announce not threaded: %+v", svc.lastIn)
	}
}

func TestJoinGroup_HappyPath(t *testing.T) {
	svc := &fakeGroupSvc{joinJID: "999@g.us"}
	h := &Handlers{Groups: svc}
	r := withTenant(chiReq(http.MethodPost, "/x", `{"invite":"abc"}`, map[string]string{"session": "s"}), testTenant)
	w := httptest.NewRecorder()
	h.JoinGroup(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestApproveGroupMembers_NotImplementedPropagates(t *testing.T) {
	svc := &fakeGroupSvc{err: domain.ErrNotImplemented("group membership approval is not implemented yet")}
	h := &Handlers{Groups: svc}
	r := withTenant(chiReq(http.MethodPost, "/x", `{"participants":["a@s.whatsapp.net"]}`,
		map[string]string{"session": "s", "gid": "g@g.us"}), testTenant)
	w := httptest.NewRecorder()
	h.ApproveGroupMembers(w, r)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

func TestCreateChannel_HappyPath(t *testing.T) {
	svc := &fakeChannelSvc{jid: "123@newsletter"}
	h := &Handlers{Channels: svc}
	r := withTenant(chiReq(http.MethodPost, "/x", `{"name":"News"}`, map[string]string{"session": "s"}), testTenant)
	w := httptest.NewRecorder()
	h.CreateChannel(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
}

func TestCreateChannel_NotImplementedPropagates(t *testing.T) {
	svc := &fakeChannelSvc{err: domain.ErrNotImplemented("channel create is not implemented yet")}
	h := &Handlers{Channels: svc}
	r := withTenant(chiReq(http.MethodPost, "/x", `{"name":"News"}`, map[string]string{"session": "s"}), testTenant)
	w := httptest.NewRecorder()
	h.CreateChannel(w, r)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
}

func TestMuteChannel_DefaultsToMute(t *testing.T) {
	svc := &fakeChannelSvc{}
	h := &Handlers{Channels: svc}
	r := withTenant(chiReq(http.MethodPost, "/x", `{}`, map[string]string{"session": "s", "jid": "c@newsletter"}), testTenant)
	w := httptest.NewRecorder()
	h.MuteChannel(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastMute == nil || !*svc.lastMute {
		t.Errorf("mute should default true: %+v", svc.lastMute)
	}
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func TestPostStatus_TextHappyPath(t *testing.T) {
	svc := &fakeStatusSvc{id: "WAMSG1"}
	h := &Handlers{Status: svc}
	r := withTenant(chiReq(http.MethodPost, "/x", `{"type":"text","text":"hi"}`, map[string]string{"session": "s"}), testTenant)
	w := httptest.NewRecorder()
	h.PostStatus(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestPostStatus_Image501(t *testing.T) {
	h := &Handlers{Status: &fakeStatusSvc{}}
	r := withTenant(chiReq(http.MethodPost, "/x", `{"type":"image"}`, map[string]string{"session": "s"}), testTenant)
	w := httptest.NewRecorder()
	h.PostStatus(w, r)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", w.Code, w.Body.String())
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeNotImplemented {
		t.Errorf("code = %q, want %q", got, domain.CodeNotImplemented)
	}
}

func TestPostStatus_NoTenant401(t *testing.T) {
	h := &Handlers{Status: &fakeStatusSvc{}}
	r := chiReq(http.MethodPost, "/x", `{"type":"text","text":"hi"}`, map[string]string{"session": "s"})
	w := httptest.NewRecorder()
	h.PostStatus(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Presence
// ---------------------------------------------------------------------------

func TestSetPresence_HappyPath(t *testing.T) {
	svc := &fakePresenceSvc{}
	h := &Handlers{Presence: svc}
	r := withTenant(chiReq(http.MethodPut, "/x", `{"state":"online"}`, map[string]string{"session": "sess_1"}), testTenant)
	w := httptest.NewRecorder()
	h.SetPresence(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if svc.lastSt != "online" || svc.lastSess != "sess_1" {
		t.Errorf("state/session not threaded: %q %q", svc.lastSt, svc.lastSess)
	}
}

func TestSetPresence_ValidationPropagates(t *testing.T) {
	svc := &fakePresenceSvc{err: domain.ErrValidation("state must be online or offline")}
	h := &Handlers{Presence: svc}
	r := withTenant(chiReq(http.MethodPut, "/x", `{"state":"bogus"}`, map[string]string{"session": "s"}), testTenant)
	w := httptest.NewRecorder()
	h.SetPresence(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
