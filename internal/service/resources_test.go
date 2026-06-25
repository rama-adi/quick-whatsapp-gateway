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
// tenantID (or a different tenant when owner != tenantID).
func expectSession(mock sqlmock.Sqlmock, sessionID, owner string) {
	cols := []string{
		"id", "tenant_id", "label", "status", "wa_jid", "wa_lid", "phone_number",
		"is_admin_session", "auto_read", "presence_typing", "rate_per_min", "rate_per_hour",
		"last_connected_at", "created_at", "updated_at",
	}
	rows := sqlmock.NewRows(cols).AddRow(
		sessionID, owner, nil, domain.SessionWorking, nil, nil, nil,
		false, false, false, 20, 200, nil, int64(1), int64(1),
	)
	mock.ExpectQuery("FROM wa_sessions").WithArgs(sessionID).WillReturnRows(rows)
}

// --- fake live ports ---

type fakePresenceController struct {
	state     string
	chatState string
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
// Tenant ownership
// ---------------------------------------------------------------------------

func TestPresenceService_RejectsForeignTenant(t *testing.T) {
	st, mock := newStore(t)
	expectSession(mock, "sess_1", "other_tenant")
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

// ---------------------------------------------------------------------------
// Group
// ---------------------------------------------------------------------------

func TestGroupService_Create_RequiresName(t *testing.T) {
	st, _ := newStore(t)
	svc := NewGroupService(st, &fakeGroupOps{}, nil)
	_, err := svc.Create(context.Background(), "ten_1", "sess_1", "", nil)
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("err = %v, want validation_error", err)
	}
}

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
