package service

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

type fakeSessionStateEngine struct {
	organizationID string
	sessionID      string
	state          application.SessionState
	err            error
	calls          int
}

func (f *fakeSessionStateEngine) GetSessionState(_ context.Context, organizationID, sessionID string) (application.SessionState, error) {
	f.calls++
	f.organizationID, f.sessionID = organizationID, sessionID
	return f.state, f.err
}

func (f *fakeSessionStateEngine) SetAccountPresence(context.Context, string, string, application.AccountPresence) error {
	return nil
}

func sessionRowForLiveState(id, org, gatewayID, waJID string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "organization_id", "created_by_user_id", "gateway_id", "label", "status",
		"wa_jid", "wa_lid", "phone_number", "is_admin_session", "auto_read", "presence_typing",
		"rate_per_min", "rate_per_hour", "last_connected_at", "created_at", "updated_at",
	}).AddRow(id, org, nil, gatewayID, nil, domain.SessionWorking, waJID, nil, nil, false, false, false, 20, 200, nil, int64(1), int64(1))
}

func TestSessionServiceMeUsesEngineAfterRepositoryOwnership(t *testing.T) {
	st, mock := newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(sessionRowForLiveState("sess_1", "org_1", "gw_1", "6281@s.whatsapp.net"))
	engine := &fakeSessionStateEngine{state: application.SessionState{OrganizationID: "org_1", SessionID: "sess_1", GatewayID: "gw_1", Status: domain.SessionStarting, Connected: false, LoggedIn: true}}
	svc := NewSessionService(st.Sessions, nil, nil)
	svc.SetGatewayLiveFacade(engine)
	got, err := svc.Me(context.Background(), "org_1", "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if engine.calls != 1 || engine.organizationID != "org_1" || engine.sessionID != "sess_1" {
		t.Fatalf("engine query org=%q session=%q calls=%d", engine.organizationID, engine.sessionID, engine.calls)
	}
	if got.Status != domain.SessionStarting || got.Connected {
		t.Fatalf("Me = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionServiceMeRejectsForeignOwnerBeforeEngine(t *testing.T) {
	st, mock := newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(sessionRowForLiveState("sess_1", "other_org", "gw_1", "6281@s.whatsapp.net"))
	engine := &fakeSessionStateEngine{}
	svc := NewSessionService(st.Sessions, nil, nil)
	svc.SetGatewayLiveFacade(engine)
	_, err := svc.Me(context.Background(), "org_1", "sess_1")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeNotFound || engine.calls != 0 {
		t.Fatalf("err=%v engine calls=%d", err, engine.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionServiceMeRejectsMismatchedEngineState(t *testing.T) {
	st, mock := newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(sessionRowForLiveState("sess_1", "org_1", "gw_1", "6281@s.whatsapp.net"))
	svc := NewSessionService(st.Sessions, nil, nil)
	svc.SetGatewayLiveFacade(&fakeSessionStateEngine{state: application.SessionState{OrganizationID: "org_1", SessionID: "other", GatewayID: "gw_1"}})
	_, err := svc.Me(context.Background(), "org_1", "sess_1")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeConflict {
		t.Fatalf("err=%v, want conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionServiceMePreservesFacadeError(t *testing.T) {
	st, mock := newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(sessionRowForLiveState("sess_1", "org_1", "gw_1", "6281@s.whatsapp.net"))
	want := errors.New("gateway unavailable")
	svc := NewSessionService(st.Sessions, nil, nil)
	svc.SetGatewayLiveFacade(&fakeSessionStateEngine{err: want})
	if _, err := svc.Me(context.Background(), "org_1", "sess_1"); !errors.Is(err, want) {
		t.Fatalf("err=%v, want facade cause", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ---- Increment 7 session-lifecycle facade preference ----
//
// With the GatewaySessionFacade set, the live parts of QR / pairing-code /
// logout must execute through it even when no in-process manager exists (which
// would otherwise answer errLiveUnavailable). The paired-session guard still
// runs first from the row.

type fakeLifecycleFacade struct {
	preparedOrg, preparedSession string
	qrOrg, qrSession             string
	pairSnap                     application.PairingSnapshot
	codeOrg, codeSession, phone  string
	code                         string
	logoutOrg, logoutSession     string
	forgetOrg, forgetSession     string
	err                          error
}

func (f *fakeLifecycleFacade) Prepare(ctx context.Context, organizationID, sessionID string) error {
	f.preparedOrg, f.preparedSession = organizationID, sessionID
	return f.err
}

func (f *fakeLifecycleFacade) QR(_ context.Context, organizationID, sessionID string) (application.PairingSnapshot, error) {
	f.qrOrg, f.qrSession = organizationID, sessionID
	return f.pairSnap, f.err
}

func (f *fakeLifecycleFacade) PairingCode(_ context.Context, organizationID, sessionID, phone string) (string, error) {
	f.codeOrg, f.codeSession, f.phone = organizationID, sessionID, phone
	return f.code, f.err
}

func (f *fakeLifecycleFacade) Logout(_ context.Context, organizationID, sessionID string) error {
	f.logoutOrg, f.logoutSession = organizationID, sessionID
	return f.err
}

func (f *fakeLifecycleFacade) Forget(_ context.Context, organizationID, sessionID string) error {
	f.forgetOrg, f.forgetSession = organizationID, sessionID
	return f.err
}

func unpairedSessionRow() *sqlmock.Rows {
	// wa_jid is SQL NULL so the row reads as unpaired (an empty string would
	// scan as a set JID).
	return sqlmock.NewRows([]string{
		"id", "organization_id", "created_by_user_id", "gateway_id", "label", "status",
		"wa_jid", "wa_lid", "phone_number", "is_admin_session", "auto_read", "presence_typing",
		"rate_per_min", "rate_per_hour", "last_connected_at", "created_at", "updated_at",
	}).AddRow("sess_1", "org_1", nil, "gw_1", nil, domain.SessionScanQR, nil, nil, nil, false, false, false, 20, 200, nil, int64(1), int64(1))
}

func TestSessionServiceQRFavorsFacadeOverMissingManager(t *testing.T) {
	st, mock := newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(unpairedSessionRow())
	facade := &fakeLifecycleFacade{pairSnap: application.PairingSnapshot{Code: "QR-1", ExpiresAt: 999}}
	svc := NewSessionService(st.Sessions, nil, nil)
	svc.SetGatewaySessionFacade(facade)
	got, err := svc.QR(context.Background(), "org_1", "sess_1")
	if err != nil {
		t.Fatalf("QR: %v", err)
	}
	if got.Code != "QR-1" || got.ExpiresAt != 999 {
		t.Fatalf("QR = %#v", got)
	}
	if facade.qrOrg != "org_1" || facade.qrSession != "sess_1" {
		t.Fatalf("facade call = %q/%q", facade.qrOrg, facade.qrSession)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionServiceLogoutFavorsFacadeOverMissingManager(t *testing.T) {
	st, mock := newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(sessionRowForLiveState("sess_1", "org_1", "gw_1", "6281@s.whatsapp.net"))
	mock.ExpectExec("UPDATE wa_sessions").WithArgs(sqlmock.AnyArg(), "sess_1").WillReturnResult(sqlmock.NewResult(0, 1))
	facade := &fakeLifecycleFacade{}
	svc := NewSessionService(st.Sessions, nil, nil)
	svc.SetGatewaySessionFacade(facade)
	if err := svc.Logout(context.Background(), "org_1", "sess_1"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if facade.logoutOrg != "org_1" || facade.logoutSession != "sess_1" {
		t.Fatalf("facade call = %q/%q", facade.logoutOrg, facade.logoutSession)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionServicePairingCodeFavorsFacadeAndValidatesPhoneFirst(t *testing.T) {
	st, mock := newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(unpairedSessionRow())
	facade := &fakeLifecycleFacade{code: "ABCD-1234"}
	svc := NewSessionService(st.Sessions, nil, nil)
	svc.SetGatewaySessionFacade(facade)
	code, err := svc.PairingCode(context.Background(), "org_1", "sess_1", "+628123")
	if err != nil || code != "ABCD-1234" {
		t.Fatalf("PairingCode = %q, %v", code, err)
	}
	if facade.phone != "+628123" || facade.codeOrg != "org_1" || facade.codeSession != "sess_1" {
		t.Fatalf("facade call = %q/%q/%q", facade.codeOrg, facade.codeSession, facade.phone)
	}
	if _, err := svc.PairingCode(context.Background(), "org_1", "sess_1", ""); !errors.As(err, new(*domain.APIError)) {
		t.Fatal("missing phone bypassed validation")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
