package service

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
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

type fakeLifecycleFacade struct {
	calls                        []string
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
	f.calls = append(f.calls, "prepare")
	f.preparedOrg, f.preparedSession = organizationID, sessionID
	return f.err
}

func (f *fakeLifecycleFacade) QR(_ context.Context, organizationID, sessionID string) (application.PairingSnapshot, error) {
	f.calls = append(f.calls, "qr")
	f.qrOrg, f.qrSession = organizationID, sessionID
	return f.pairSnap, f.err
}

func (f *fakeLifecycleFacade) PairingCode(_ context.Context, organizationID, sessionID, phone string) (string, error) {
	f.codeOrg, f.codeSession, f.phone = organizationID, sessionID, phone
	return f.code, f.err
}

func (f *fakeLifecycleFacade) Logout(_ context.Context, organizationID, sessionID string) error {
	f.calls = append(f.calls, "logout")
	f.logoutOrg, f.logoutSession = organizationID, sessionID
	return f.err
}

func (f *fakeLifecycleFacade) Forget(_ context.Context, organizationID, sessionID string) error {
	f.calls = append(f.calls, "forget")
	f.forgetOrg, f.forgetSession = organizationID, sessionID
	return f.err
}

type fakeSessionDesiredController struct {
	calls []bool
	err   error
}

func isLiveUnavailable(err error) bool {
	var apiErr *domain.APIError
	return errors.As(err, &apiErr) && apiErr.Code == domain.CodeNotImplemented
}

func (f *fakeSessionDesiredController) SetSessionDesired(_ context.Context, _ string, run bool) error {
	f.calls = append(f.calls, run)
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

func TestSessionServiceRestartDoesNotSetDesiredAfterForgetFailure(t *testing.T) {
	st, mock := newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(sessionRowForLiveState("sess_1", "org_1", "gw_1", "6281@s.whatsapp.net"))
	wantErr := errors.New("gateway unavailable")
	facade := &fakeLifecycleFacade{err: wantErr}
	desired := &fakeSessionDesiredController{}
	svc := NewSessionService(st.Sessions, nil, nil)
	svc.SetGatewaySessionFacade(facade)
	svc.SetSessionDesiredController(desired)

	if err := svc.Restart(context.Background(), "org_1", "sess_1"); !errors.Is(err, wantErr) {
		t.Fatalf("Restart error = %v, want %v", err, wantErr)
	}
	if len(desired.calls) != 0 {
		t.Fatalf("desired calls = %v, want none", desired.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionServiceLifecycleWithoutDesiredControllerReturnsUnavailable(t *testing.T) {
	st, mock := newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(unpairedSessionRow())
	svc := NewSessionService(st.Sessions, nil, nil)

	if err := svc.Start(context.Background(), "org_1", "sess_1"); !isLiveUnavailable(err) {
		t.Fatalf("Start error = %v, want live unavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	st, mock = newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(unpairedSessionRow())
	svc = NewSessionService(st.Sessions, nil, nil)
	if err := svc.Stop(context.Background(), "org_1", "sess_1"); !isLiveUnavailable(err) {
		t.Fatalf("Stop error = %v, want live unavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	st, mock = newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(unpairedSessionRow())
	svc = NewSessionService(st.Sessions, nil, nil)
	if err := svc.Restart(context.Background(), "org_1", "sess_1"); !isLiveUnavailable(err) {
		t.Fatalf("Restart error = %v, want live unavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
