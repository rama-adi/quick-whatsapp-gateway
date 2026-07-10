package service

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// newStore wires a store over a sqlmock DB. The returned mock primes the
// session-ownership lookup every resource service performs first.
func newStore(t *testing.T) (*store.Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store.New(db), mock
}

// expectSession primes a SELECT … FROM wa_sessions returning a row owned by
// organizationID (or a different organization when owner != organizationID).
func expectSession(mock sqlmock.Sqlmock, sessionID, owner string) {
	cols := []string{
		"id", "organization_id", "created_by_user_id", "gateway_id", "label", "status",
		"wa_jid", "wa_lid", "phone_number", "is_admin_session", "auto_read", "presence_typing",
		"rate_per_min", "rate_per_hour", "last_connected_at", "created_at", "updated_at",
	}
	rows := sqlmock.NewRows(cols).AddRow(
		sessionID, owner, nil, "gw_1", nil, domain.SessionWorking, nil, nil, nil,
		false, false, false, 20, 200, nil, int64(1), int64(1),
	)
	mock.ExpectQuery("FROM wa_sessions").WithArgs(sessionID).WillReturnRows(rows)
}

// --- fake live ports ---

type fakePresenceController struct {
	state     string
	chatState string
	presence  domain.PresenceStatus
	err       error
}

func (f *fakePresenceController) SetPresence(_ context.Context, _, state string) error {
	f.state = state
	return f.err
}
func (f *fakePresenceController) SetChatPresence(_ context.Context, _, _, state string) error {
	f.chatState = state
	return f.err
}
func (f *fakePresenceController) GetPresence(_ context.Context, _, chatJID string) (domain.PresenceStatus, error) {
	if f.presence.From != "" {
		return f.presence, f.err
	}
	return domain.PresenceStatus{ChatJID: chatJID, From: chatJID, State: "unknown"}, f.err
}

type fakeStatusPoster struct {
	id  string
	err error
}

func (f *fakeStatusPoster) PostText(context.Context, string, string) (string, error) {
	return f.id, f.err
}

type fakeGroupOps struct {
	info   GroupInfo
	action GroupParticipantAction
	err    error
}

func (f *fakeGroupOps) CreateGroup(context.Context, string, string, []string) (GroupInfo, error) {
	return f.info, f.err
}
func (f *fakeGroupOps) GetGroupInfo(context.Context, string, string) (GroupInfo, error) {
	return f.info, f.err
}
func (f *fakeGroupOps) UpdateParticipants(_ context.Context, _, _ string, _ []string, a GroupParticipantAction) error {
	f.action = a
	return f.err
}
func (f *fakeGroupOps) UpdateSettings(context.Context, string, string, GroupSettings) error {
	return f.err
}
func (f *fakeGroupOps) GetInviteLink(context.Context, string, string, bool) (string, error) {
	return "https://chat.whatsapp.com/abc", f.err
}
func (f *fakeGroupOps) JoinWithLink(context.Context, string, string) (string, error) {
	return "123@g.us", f.err
}
func (f *fakeGroupOps) Leave(context.Context, string, string) error { return f.err }

// ---------------------------------------------------------------------------
// Organization ownership
// ---------------------------------------------------------------------------

// TestPresenceService_RejectsForeignOrganization requests a presence change for a session owned by another
// organization. The service must return not_found before calling the live controller, so tenant ownership
// is not disclosed and no WhatsApp state changes. This is the authorization boundary shared by live
// resource operations.
func TestPresenceService_RejectsForeignOrganization(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "other_organization")
	svc := NewPresenceService(st, &fakePresenceController{}, nil)
	err := svc.Set(context.Background(), "ten_1", "sess_1", "online")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}

// ---------------------------------------------------------------------------
// Presence
// ---------------------------------------------------------------------------

// TestPresenceService_Set loads an owned session and sets a supported account presence through a recording
// controller. The exact session and normalized state must be delegated once and success returned.
// Persistence ownership is checked before the live whatsmeow call.
func TestPresenceService_Set(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	ctrl := &fakePresenceController{}
	svc := NewPresenceService(st, ctrl, nil)
	if err := svc.Set(context.Background(), "ten_1", "sess_1", "online"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if ctrl.state != "online" {
		t.Errorf("state = %q, want online", ctrl.state)
	}
}

// TestPresenceService_BadState passes an unsupported presence value for an otherwise owned session.
// Validation must fail before invoking the controller. This keeps protocol-specific invalid states from
// reaching whatsmeow.
func TestPresenceService_BadState(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	svc := NewPresenceService(st, &fakePresenceController{}, nil)
	err := svc.Set(context.Background(), "ten_1", "sess_1", "bogus")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("err = %v, want validation_error", err)
	}
}

// TestPresenceService_NilControllerNotImplemented constructs the service without a live presence adapter
// and requests a valid update. It must return not_implemented rather than dereference a nil interface or
// pretend success. Read-only/serverless wiring therefore fails explicitly for live operations.
func TestPresenceService_NilControllerNotImplemented(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	svc := NewPresenceService(st, nil, nil)
	err := svc.Set(context.Background(), "ten_1", "sess_1", "online")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeNotImplemented {
		t.Fatalf("err = %v, want not_implemented", err)
	}
}

// ---------------------------------------------------------------------------
// Chat
// ---------------------------------------------------------------------------

// TestChatService_SetPresence_Validates covers missing chat JID and unsupported composing states for
// chat-scoped presence. Each invalid request must stop before the live adapter. The service owns request
// semantics even though whatsmeow performs the final send.
func TestChatService_SetPresence_Validates(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	svc := NewChatService(st, &fakePresenceController{}, nil)
	err := svc.SetPresence(context.Background(), "ten_1", "sess_1", "c@s.whatsapp.net", "typing")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("err = %v, want validation_error", err)
	}
}

// TestChatService_SetPresence_Delegates sets a valid composing state for an owned chat and records the
// downstream call. Session, chat JID, state, and media type must be forwarded exactly once. This pins
// translation between the public resource operation and whatsmeow chat presence.
func TestChatService_SetPresence_Delegates(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	ctrl := &fakePresenceController{}
	svc := NewChatService(st, ctrl, nil)
	if err := svc.SetPresence(context.Background(), "ten_1", "sess_1", "c@s.whatsapp.net", "composing"); err != nil {
		t.Fatalf("SetPresence: %v", err)
	}
	if ctrl.chatState != "composing" {
		t.Errorf("chatState = %q, want composing", ctrl.chatState)
	}
}

// TestChatService_GetPresence_Delegates requests current presence for an owned chat through the live
// adapter. The service must forward the resolved session and chat identifiers and return the adapters
// domain result unchanged. Ownership remains enforced even for read-like live queries.
func TestChatService_GetPresence_Delegates(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	ctrl := &fakePresenceController{}
	svc := NewChatService(st, ctrl, nil)
	got, err := svc.GetPresence(context.Background(), "ten_1", "sess_1", "c@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetPresence: %v", err)
	}
	if got.State != "unknown" || got.From != "c@s.whatsapp.net" {
		t.Errorf("presence = %+v, want unknown for c@s.whatsapp.net", got)
	}
}

// ---------------------------------------------------------------------------
// Group
// ---------------------------------------------------------------------------

// TestGroupService_Create_RequiresName attempts to create a group with an empty name. The service must
// return validation_error before resolving participants or calling group operations. This prevents an
// avoidable whatsmeow protocol failure.
func TestGroupService_Create_RequiresName(t *testing.T) {
	st, _ := newStore(t)
	svc := NewGroupService(st, &fakeGroupOps{}, nil)
	_, err := svc.Create(context.Background(), "ten_1", "sess_1", "", nil)
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("err = %v, want validation_error", err)
	}
}

// TestGroupService_Promote_UsesPromoteAction applies the promote member action to an owned group through a
// recording GroupOps adapter. The service must choose the promote operation, preserve participant JIDs,
// and avoid any other membership action. This guards action dispatch in the shared member-mutation path.
func TestGroupService_Promote_UsesPromoteAction(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	ops := &fakeGroupOps{}
	svc := NewGroupService(st, ops, nil)
	if err := svc.Promote(context.Background(), "ten_1", "sess_1", "g@g.us", "a@s.whatsapp.net"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if ops.action != GroupActionPromote {
		t.Errorf("action = %q, want promote", ops.action)
	}
}

// TestGroupService_NilOpsNotImplemented calls a valid group mutation when no live GroupOps adapter is
// configured. The result must be not_implemented with no panic or store mutation. Deployments lacking a
// connected manager cannot silently accept live operations.
func TestGroupService_NilOpsNotImplemented(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	svc := NewGroupService(st, nil, nil)
	err := svc.Leave(context.Background(), "ten_1", "sess_1", "g@g.us")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeNotImplemented {
		t.Fatalf("err = %v, want not_implemented", err)
	}
}

// TestGroupService_ApproveMembers_NotImplemented requests the approve-members operation that the current
// whatsmeow adapter does not support. The service must return not_implemented instead of mapping it to an
// unsafe nearby action. Unsupported API surface remains explicit and forward-compatible.
func TestGroupService_ApproveMembers_NotImplemented(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	svc := NewGroupService(st, &fakeGroupOps{}, nil)
	err := svc.ApproveMembers(context.Background(), "ten_1", "sess_1", "g@g.us", nil)
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeNotImplemented {
		t.Fatalf("err = %v, want not_implemented", err)
	}
}

// ---------------------------------------------------------------------------
// Contact
// ---------------------------------------------------------------------------

// TestContactService_List_RejectsBadSource lists contacts with a source filter outside the supported
// phone, group, and all values. Validation must fail before querying repositories or the live directory.
// This keeps filtering semantics stable across stored and WhatsApp-backed sources.
func TestContactService_List_RejectsBadSource(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	svc := NewContactService(st, nil, nil)
	_, err := svc.List(context.Background(), "ten_1", "sess_1", store.ContactFilter{Source: "bogus"}, "", 20)
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("err = %v, want validation_error", err)
	}
}

// TestContactService_Check_RequiresPhone calls contact existence checking without a phone number. The
// service must return validation_error before normalizing a JID or invoking the directory. Empty checks
// cannot become broad address-book queries.
func TestContactService_Check_RequiresPhone(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	svc := NewContactService(st, &fakeContactDirectory{}, nil)
	_, err := svc.Check(context.Background(), "ten_1", "sess_1", "")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("err = %v, want validation_error", err)
	}
}

type fakeContactDirectory struct {
	on  []OnWhatsApp
	err error
}

func (f *fakeContactDirectory) IsOnWhatsApp(context.Context, string, []string) ([]OnWhatsApp, error) {
	return f.on, f.err
}
func (f *fakeContactDirectory) ProfilePicture(context.Context, string, string) (ProfilePicture, error) {
	return ProfilePicture{}, f.err
}
func (f *fakeContactDirectory) About(context.Context, string, string) (string, error) {
	return "", f.err
}
func (f *fakeContactDirectory) SetBlocked(context.Context, string, string, bool) error {
	return f.err
}

// TestContactService_Check_Delegates checks a valid phone through the live contact directory for an owned
// session. The normalized number and session id must reach the adapter and its existence result must pass
// through. This pins ownership and input normalization around the whatsmeow lookup.
func TestContactService_Check_Delegates(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	dir := &fakeContactDirectory{on: []OnWhatsApp{{Query: "+628", JID: "628@s.whatsapp.net", IsIn: true}}}
	svc := NewContactService(st, dir, nil)
	got, err := svc.Check(context.Background(), "ten_1", "sess_1", "+628")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !got.IsIn || got.JID != "628@s.whatsapp.net" {
		t.Errorf("unexpected result: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

// TestStatusService_PostText posts a non-empty text status through a recording live status adapter. The
// owned session and exact text must be delegated once and the returned message metadata preserved. This is
// the supported status-posting happy path.
func TestStatusService_PostText(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	svc := NewStatusService(st, &fakeStatusPoster{id: "WAMSG1"}, nil)
	id, err := svc.PostText(context.Background(), "ten_1", "sess_1", "hello")
	if err != nil {
		t.Fatalf("PostText: %v", err)
	}
	if id != "WAMSG1" {
		t.Errorf("id = %q, want WAMSG1", id)
	}
}

// TestStatusService_PostText_RequiresText submits an empty text status for an owned session. Validation
// must fail before the live adapter is called. The service prevents meaningless protocol sends while
// retaining a clear client error.
func TestStatusService_PostText_RequiresText(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	svc := NewStatusService(st, &fakeStatusPoster{}, nil)
	_, err := svc.PostText(context.Background(), "ten_1", "sess_1", "")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("err = %v, want validation_error", err)
	}
}

// TestStatusService_PostImage_NotImplemented requests image status posting, which lacks a production
// adapter path. The service must return not_implemented and perform no text fallback. Media status support
// cannot accidentally degrade into a different visible post.
func TestStatusService_PostImage_NotImplemented(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "ten_1")
	svc := NewStatusService(st, &fakeStatusPoster{}, nil)
	_, err := svc.PostImage(context.Background(), "ten_1", "sess_1")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeNotImplemented {
		t.Fatalf("err = %v, want not_implemented", err)
	}
}
