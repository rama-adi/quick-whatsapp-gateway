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
