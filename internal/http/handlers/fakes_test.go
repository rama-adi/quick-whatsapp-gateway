package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// testOrganization is the organization id injected into request contexts by withOrganization.
const testOrganization = "ten_test"

// withOrganization returns r with the organization id set on its context, mirroring what the
// auth middleware does in production.
func withOrganization(r *http.Request, organizationID string) *http.Request {
	return r.WithContext(httpx.SetOrganizationID(r.Context(), organizationID))
}

// chiReq builds a request whose chi RouteContext carries the given URL params,
// so handlers reading chi.URLParam see them without a full router.
func chiReq(method, target string, body string, params map[string]string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// decodeError unmarshals the §11 error envelope from a response body.
func decodeError(body string) domain.ErrorBody {
	var b domain.ErrorBody
	_ = json.Unmarshal([]byte(body), &b)
	return b
}

// --- Fake SessionSvc ---

type fakeSessionSvc struct {
	created   domain.WASession
	list      []domain.WASession
	one       domain.WASession
	me        service.Me
	qr        service.QR
	code      string
	err       error
	createIn  service.CreateInput
	lastID    string
	lastPhone string
	startErr  error
}

func (f *fakeSessionSvc) Create(_ context.Context, _ string, in service.CreateInput) (domain.WASession, error) {
	f.createIn = in
	return f.created, f.err
}
func (f *fakeSessionSvc) List(context.Context, string) ([]domain.WASession, error) {
	return f.list, f.err
}
func (f *fakeSessionSvc) Get(_ context.Context, _, id string) (domain.WASession, error) {
	f.lastID = id
	return f.one, f.err
}
func (f *fakeSessionSvc) Start(_ context.Context, _, id string) error {
	f.lastID = id
	return f.startErr
}
func (f *fakeSessionSvc) Stop(_ context.Context, _, id string) error {
	f.lastID = id
	return f.startErr
}
func (f *fakeSessionSvc) Restart(_ context.Context, _, id string) error {
	f.lastID = id
	return f.startErr
}
func (f *fakeSessionSvc) Logout(_ context.Context, _, id string) error {
	f.lastID = id
	return f.startErr
}
func (f *fakeSessionSvc) Delete(_ context.Context, _, id string) error { f.lastID = id; return f.err }
func (f *fakeSessionSvc) Me(_ context.Context, _, id string) (service.Me, error) {
	f.lastID = id
	return f.me, f.err
}
func (f *fakeSessionSvc) QR(_ context.Context, _, id string) (service.QR, error) {
	f.lastID = id
	return f.qr, f.err
}
func (f *fakeSessionSvc) PairingCode(_ context.Context, _, id, phone string) (string, error) {
	f.lastID = id
	f.lastPhone = phone
	return f.code, f.err
}

// --- Fake MessageSvc ---

type fakeMessageSvc struct {
	result   outbound.SendResult
	err      error
	lastReq  domain.SendRequest
	lastOpts outbound.SendOptions
	lastOp   string
}

func (f *fakeMessageSvc) Send(_ context.Context, _, _ string, req domain.SendRequest, opts outbound.SendOptions) (outbound.SendResult, error) {
	f.lastReq = req
	f.lastOpts = opts
	return f.result, f.err
}
func (f *fakeMessageSvc) Edit(_ context.Context, _, _, _, _, _ string) (outbound.SendResult, error) {
	f.lastOp = "edit"
	return f.result, f.err
}
func (f *fakeMessageSvc) Revoke(_ context.Context, _, _, _, _, _ string) (outbound.SendResult, error) {
	f.lastOp = "revoke"
	return f.result, f.err
}
func (f *fakeMessageSvc) React(_ context.Context, _, _, _, _, _, emoji string) (outbound.SendResult, error) {
	f.lastOp = "react:" + emoji
	return f.result, f.err
}
func (f *fakeMessageSvc) Forward(_ context.Context, _, _, _, _, _, _ string) (outbound.SendResult, error) {
	f.lastOp = "forward"
	return f.result, f.err
}
func (f *fakeMessageSvc) Vote(_ context.Context, _, _, _, _, _ string, _ []string) (outbound.SendResult, error) {
	f.lastOp = "vote"
	return f.result, f.err
}

// --- Fake WebhookSvc ---

type fakeWebhookSvc struct {
	created domain.Webhook
	list    []domain.Webhook
	one     domain.Webhook
	updated domain.Webhook
	err     error
	lastIn  service.WebhookInput
}

func (f *fakeWebhookSvc) Create(_ context.Context, _ string, in service.WebhookInput) (domain.Webhook, error) {
	f.lastIn = in
	return f.created, f.err
}
func (f *fakeWebhookSvc) List(context.Context, string) ([]domain.Webhook, error) {
	return f.list, f.err
}
func (f *fakeWebhookSvc) Get(context.Context, string, string) (domain.Webhook, error) {
	return f.one, f.err
}
func (f *fakeWebhookSvc) Update(_ context.Context, _, _ string, in service.WebhookInput) (domain.Webhook, error) {
	f.lastIn = in
	return f.updated, f.err
}
func (f *fakeWebhookSvc) Delete(context.Context, string, string) error { return f.err }

// --- Fake AdminSvc ---

type fakeAdminSvc struct {
	job  domain.BackfillJob
	list []domain.WASession
	err  error
}

func (f *fakeAdminSvc) ListAllSessions(context.Context) ([]domain.WASession, error) {
	return f.list, f.err
}
func (f *fakeAdminSvc) StartBackfill(context.Context, string) (domain.BackfillJob, error) {
	return f.job, f.err
}
func (f *fakeAdminSvc) BackfillStatus(context.Context, string) (domain.BackfillJob, error) {
	return f.job, f.err
}

// --- Fake ChatSvc ---

type fakeChatSvc struct {
	chats    store.Page[domain.Chat]
	one      domain.Chat
	messages store.Page[domain.Message]
	err      error
	lastIn   service.ChatUpdate
	lastID   string
	lastCID  string
	lastSt   string
}

func (f *fakeChatSvc) List(_ context.Context, _, sessionID, _ string, _ int) (store.Page[domain.Chat], error) {
	f.lastID = sessionID
	return f.chats, f.err
}
func (f *fakeChatSvc) Get(_ context.Context, _, _, cid string) (domain.Chat, error) {
	f.lastCID = cid
	return f.one, f.err
}
func (f *fakeChatSvc) ListMessages(_ context.Context, _, _, cid, _ string, _ int) (store.Page[domain.Message], error) {
	f.lastCID = cid
	return f.messages, f.err
}
func (f *fakeChatSvc) Read(_ context.Context, _, _, cid string) (domain.Chat, error) {
	f.lastCID = cid
	return f.one, f.err
}
func (f *fakeChatSvc) Update(_ context.Context, _, _, cid string, in service.ChatUpdate) (domain.Chat, error) {
	f.lastCID = cid
	f.lastIn = in
	return f.one, f.err
}
func (f *fakeChatSvc) Delete(_ context.Context, _, _, cid string) error {
	f.lastCID = cid
	return f.err
}
func (f *fakeChatSvc) SetPresence(_ context.Context, _, _, cid, state string) error {
	f.lastCID = cid
	f.lastSt = state
	return f.err
}

// --- Fake ContactSvc ---

type fakeContactSvc struct {
	contacts store.Page[domain.Contact]
	detail   service.ContactDetail
	check    domain.OnWhatsApp
	pic      domain.ProfilePicture
	about    string
	err      error
	lastF    store.ContactFilter
	lastJID  string
	blocked  *bool
}

func (f *fakeContactSvc) List(_ context.Context, _, _ string, ff store.ContactFilter, _ string, _ int) (store.Page[domain.Contact], error) {
	f.lastF = ff
	return f.contacts, f.err
}
func (f *fakeContactSvc) Get(_ context.Context, _, _, _ string) (service.ContactDetail, error) {
	return f.detail, f.err
}
func (f *fakeContactSvc) Check(_ context.Context, _, _, _ string) (domain.OnWhatsApp, error) {
	return f.check, f.err
}
func (f *fakeContactSvc) Picture(_ context.Context, _, _, jid string) (domain.ProfilePicture, error) {
	f.lastJID = jid
	return f.pic, f.err
}
func (f *fakeContactSvc) About(_ context.Context, _, _, _ string) (string, error) {
	return f.about, f.err
}
func (f *fakeContactSvc) SetBlocked(_ context.Context, _, _, jid string, blocked bool) error {
	f.lastJID = jid
	f.blocked = &blocked
	return f.err
}

// --- Fake GroupSvc ---

type fakeGroupSvc struct {
	info    domain.GroupInfo
	groups  []domain.Group
	one     domain.Group
	members []domain.GroupMember
	invite  string
	joinJID string
	err     error
	lastOp  string
	lastIn  domain.GroupSettings
}

func (f *fakeGroupSvc) Create(_ context.Context, _, _, _ string, _ []string) (domain.GroupInfo, error) {
	f.lastOp = "create"
	return f.info, f.err
}
func (f *fakeGroupSvc) List(context.Context, string, string) ([]domain.Group, error) {
	return f.groups, f.err
}
func (f *fakeGroupSvc) Get(context.Context, string, string, string) (domain.Group, error) {
	return f.one, f.err
}
func (f *fakeGroupSvc) Members(context.Context, string, string, string) ([]domain.GroupMember, error) {
	return f.members, f.err
}
func (f *fakeGroupSvc) AddMembers(_ context.Context, _, _, _ string, _ []string) error {
	f.lastOp = "add"
	return f.err
}
func (f *fakeGroupSvc) RemoveMember(_ context.Context, _, _, _, _ string) error {
	f.lastOp = "remove"
	return f.err
}
func (f *fakeGroupSvc) Promote(_ context.Context, _, _, _, _ string) error {
	f.lastOp = "promote"
	return f.err
}
func (f *fakeGroupSvc) Demote(_ context.Context, _, _, _, _ string) error {
	f.lastOp = "demote"
	return f.err
}
func (f *fakeGroupSvc) UpdateSettings(_ context.Context, _, _, _ string, in domain.GroupSettings) error {
	f.lastOp = "update"
	f.lastIn = in
	return f.err
}
func (f *fakeGroupSvc) InviteLink(context.Context, string, string, string) (string, error) {
	return f.invite, f.err
}
func (f *fakeGroupSvc) RevokeInvite(context.Context, string, string, string) (string, error) {
	return f.invite, f.err
}
func (f *fakeGroupSvc) Join(_ context.Context, _, _, _ string) (string, error) {
	f.lastOp = "join"
	return f.joinJID, f.err
}
func (f *fakeGroupSvc) Leave(_ context.Context, _, _, _ string) error {
	f.lastOp = "leave"
	return f.err
}
func (f *fakeGroupSvc) ApproveMembers(_ context.Context, _, _, _ string, _ []string) error {
	f.lastOp = "approve"
	return f.err
}

// --- Fake ChannelSvc ---

type fakeChannelSvc struct {
	jid      string
	messages store.Page[domain.Message]
	err      error
	lastOp   string
	lastMute *bool
}

func (f *fakeChannelSvc) Create(_ context.Context, _, _, _, _ string) (string, error) {
	f.lastOp = "create"
	return f.jid, f.err
}
func (f *fakeChannelSvc) Follow(_ context.Context, _, _, _ string) error {
	f.lastOp = "follow"
	return f.err
}
func (f *fakeChannelSvc) Unfollow(_ context.Context, _, _, _ string) error {
	f.lastOp = "unfollow"
	return f.err
}
func (f *fakeChannelSvc) Mute(_ context.Context, _, _, _ string, mute bool) error {
	f.lastOp = "mute"
	f.lastMute = &mute
	return f.err
}
func (f *fakeChannelSvc) Messages(_ context.Context, _, _, _, _ string, _ int) (store.Page[domain.Message], error) {
	return f.messages, f.err
}

// --- Fake StatusSvc ---

type fakeStatusSvc struct {
	id  string
	err error
}

func (f *fakeStatusSvc) PostText(context.Context, string, string, string) (string, error) {
	return f.id, f.err
}
func (f *fakeStatusSvc) PostImage(context.Context, string, string) (string, error) {
	return "", domain.ErrNotImplemented("image status is not implemented yet")
}

// --- Fake PresenceSvc ---

type fakePresenceSvc struct {
	err      error
	lastSt   string
	lastSess string
}

func (f *fakePresenceSvc) Set(_ context.Context, _, sessionID, state string) error {
	f.lastSess = sessionID
	f.lastSt = state
	return f.err
}
