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
	return sqlmock.NewRows(sessionTestColumns).AddRow(sessionTestRow(id, org, nil, gatewayID, nil, domain.SessionWorking, waJID, nil, nil, false, false, false, 20, 200, nil, int64(1), int64(1), 30, nil, nil)...)
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
	calls                       []string
	applied                     []appliedIntent
	applyErr                    error
	qrOrg, qrSession            string
	pairSnap                    application.PairingSnapshot
	codeOrg, codeSession, phone string
	code                        string
	logoutOrg, logoutSession    string
	forgetOrg, forgetSession    string
	err                         error
}

type appliedIntent struct {
	run        bool
	generation uint64
	device     string
}

func (f *fakeLifecycleFacade) Apply(_ context.Context, _, _ string, run bool, generation uint64, device string) error {
	f.calls = append(f.calls, "apply")
	f.applied = append(f.applied, appliedIntent{run: run, generation: generation, device: device})
	return f.applyErr
}

func (f *fakeLifecycleFacade) QR(_ context.Context, organizationID, sessionID string) (application.PairingSnapshot, error) {
	f.calls = append(f.calls, "qr")
	f.qrOrg, f.qrSession = organizationID, sessionID
	return f.pairSnap, f.err
}

func (f *fakeLifecycleFacade) PairingCode(_ context.Context, organizationID, sessionID, phone string) (string, error) {
	f.calls = append(f.calls, "pairing-code")
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

func (f *fakeLifecycleFacade) SubmitPasskey(context.Context, string, string, []byte) error {
	return nil
}

func (f *fakeLifecycleFacade) ConfirmPasskey(context.Context, string, string) error {
	return nil
}

// fakeSessionState records intent writes; generation is what SetDesired
// returns (the stored start generation after the write).
type fakeSessionState struct {
	desired    []desiredWrite
	cleared    []string
	generation uint64
	err        error
}

type desiredWrite struct {
	state    domain.DesiredState
	newStart bool
}

func (f *fakeSessionState) SetDesired(_ context.Context, _ string, desired domain.DesiredState, newStart bool, _ int64) (uint64, error) {
	f.desired = append(f.desired, desiredWrite{state: desired, newStart: newStart})
	if newStart {
		f.generation++
	}
	return f.generation, f.err
}

func (f *fakeSessionState) ClearPairing(_ context.Context, sessionID string, _ int64) error {
	f.cleared = append(f.cleared, sessionID)
	return f.err
}

func isLiveUnavailable(err error) bool {
	var apiErr *domain.APIError
	return errors.As(err, &apiErr) && apiErr.Code == domain.CodeNotImplemented
}

// sessionReadRow is one session read row with explicit intent and runtime.
func sessionReadRow(waJID any, desired domain.DesiredState, generation uint64, status domain.SessionStatus, failure any, gatewayConnected bool) *sqlmock.Rows {
	return sqlmock.NewRows(sessionTestColumns).AddRow(
		"sess_1", "org_1", nil, "gw_1", nil, string(desired), generation, waJID, nil, nil, false, false,
		false, 20, 200, nil, int64(1), int64(1), 30, nil, nil, string(status), failure, int64(7), gatewayConnected,
	)
}

func lifecycleService(t *testing.T, row *sqlmock.Rows) (*SessionService, *fakeLifecycleFacade, *fakeSessionState, sqlmock.Sqlmock) {
	t.Helper()
	st, mock := newStore(t)
	mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(row)
	facade := &fakeLifecycleFacade{}
	state := &fakeSessionState{generation: 4}
	svc := NewSessionService(st.Sessions, nil, nil)
	svc.SetGatewaySessionFacade(facade)
	svc.SetSessionStateStore(state)
	return svc, facade, state, mock
}

// A stopped session gets a new start generation and the intent is applied on
// the gateway with the device it must use.
func TestSessionStartRecordsIntentWithNewGeneration(t *testing.T) {
	svc, facade, state, _ := lifecycleService(t, sessionReadRow("628@s.whatsapp.net", domain.DesiredStop, 4, domain.SessionStopped, nil, true))
	if err := svc.Start(context.Background(), "org_1", "sess_1"); err != nil {
		t.Fatal(err)
	}
	if len(state.desired) != 1 || state.desired[0] != (desiredWrite{state: domain.DesiredRun, newStart: true}) {
		t.Fatalf("desired writes = %+v", state.desired)
	}
	if len(facade.applied) != 1 || facade.applied[0] != (appliedIntent{run: true, generation: 5, device: "628@s.whatsapp.net"}) {
		t.Fatalf("applied = %+v", facade.applied)
	}
}

// Starting a running session does not restart it; starting a failed one asks
// again (new generation), which is what lets the gateway leave the latch.
func TestSessionStartBumpsGenerationOnlyAfterTheRunEnded(t *testing.T) {
	svc, _, state, _ := lifecycleService(t, sessionReadRow("628@s.whatsapp.net", domain.DesiredRun, 4, domain.SessionWorking, nil, true))
	if err := svc.Start(context.Background(), "org_1", "sess_1"); err != nil {
		t.Fatal(err)
	}
	if state.desired[0].newStart {
		t.Fatal("start of a working session asked for a new run")
	}

	svc, _, state, _ = lifecycleService(t, sessionReadRow("628@s.whatsapp.net", domain.DesiredRun, 4, domain.SessionFailed, "stream_replaced", true))
	if err := svc.Start(context.Background(), "org_1", "sess_1"); err != nil {
		t.Fatal(err)
	}
	if !state.desired[0].newStart {
		t.Fatal("start of a failed session did not ask for a new run")
	}
}

// The intent is durable before the gateway call: a gateway that cannot take
// the call now converges from its next snapshot, so the action succeeds.
func TestSessionRestartSurvivesApplyFailure(t *testing.T) {
	svc, facade, state, _ := lifecycleService(t, sessionReadRow(nil, domain.DesiredRun, 4, domain.SessionScanQR, nil, true))
	facade.applyErr = domain.ErrUnavailable("gateway unavailable")
	if err := svc.Restart(context.Background(), "org_1", "sess_1"); err != nil {
		t.Fatalf("restart failed although its intent was recorded: %v", err)
	}
	if len(state.desired) != 1 || !state.desired[0].newStart {
		t.Fatalf("restart did not ask for a new run: %+v", state.desired)
	}
}

func TestSessionStopRecordsStopAndAppliesIt(t *testing.T) {
	svc, facade, state, _ := lifecycleService(t, sessionReadRow(nil, domain.DesiredRun, 4, domain.SessionScanQR, nil, true))
	if err := svc.Stop(context.Background(), "org_1", "sess_1"); err != nil {
		t.Fatal(err)
	}
	if len(state.desired) != 1 || state.desired[0] != (desiredWrite{state: domain.DesiredStop}) {
		t.Fatalf("desired writes = %+v", state.desired)
	}
	if len(facade.applied) != 1 || facade.applied[0].run {
		t.Fatalf("applied = %+v", facade.applied)
	}
}

// GET /qr only reads: it answers ready or pending from the gateway's current
// code and never applies or starts anything.
func TestSessionQRIsSideEffectFree(t *testing.T) {
	svc, facade, state, _ := lifecycleService(t, sessionReadRow(nil, domain.DesiredRun, 4, domain.SessionStarting, nil, true))
	qr, err := svc.QR(context.Background(), "org_1", "sess_1")
	if err != nil || qr.State != QRPending || qr.Code != "" {
		t.Fatalf("pending QR = %+v, %v", qr, err)
	}
	if len(state.desired) != 0 || len(facade.applied) != 0 || facade.calls[0] != "qr" {
		t.Fatalf("QR read had side effects: desired=%+v calls=%v", state.desired, facade.calls)
	}

	svc, facade, _, _ = lifecycleService(t, sessionReadRow(nil, domain.DesiredRun, 4, domain.SessionScanQR, nil, true))
	facade.pairSnap = application.PairingSnapshot{Code: "2@code", ExpiresAt: 99}
	qr, err = svc.QR(context.Background(), "org_1", "sess_1")
	if err != nil || qr != (QR{State: QRReady, Code: "2@code", ExpiresAt: 99}) {
		t.Fatalf("ready QR = %+v, %v", qr, err)
	}
}

// A failed pairing answers 409 with its reason without touching the gateway,
// instead of reopening a doomed WhatsApp connection on every poll.
func TestSessionQRFailedPairingIsConflictWithReason(t *testing.T) {
	svc, facade, _, _ := lifecycleService(t, sessionReadRow(nil, domain.DesiredRun, 4, domain.SessionFailed, "qr_timeout", true))
	_, err := svc.QR(context.Background(), "org_1", "sess_1")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeConflict || apiErr.Details["failureReason"] != "qr_timeout" {
		t.Fatalf("failed pairing QR error = %v", err)
	}
	if len(facade.calls) != 0 {
		t.Fatalf("failed pairing reached the gateway: %v", facade.calls)
	}

	svc, facade, _, _ = lifecycleService(t, sessionReadRow(nil, domain.DesiredStop, 4, domain.SessionStopped, nil, true))
	if _, err := svc.QR(context.Background(), "org_1", "sess_1"); !errors.As(err, &apiErr) || apiErr.Code != domain.CodeConflict {
		t.Fatalf("stopped session QR error = %v", err)
	}
	if len(facade.calls) != 0 {
		t.Fatalf("stopped session reached the gateway: %v", facade.calls)
	}
}

// A live status on a gateway that is not connected reads as unavailable, and
// the QR read answers 503 instead of a stale code.
func TestSessionStatusUnavailableWhenGatewayDisconnected(t *testing.T) {
	svc, _, _, _ := lifecycleService(t, sessionReadRow(nil, domain.DesiredRun, 4, domain.SessionScanQR, nil, false))
	sess, err := svc.Get(context.Background(), "org_1", "sess_1")
	if err != nil || sess.Status != domain.SessionUnavailable {
		t.Fatalf("status = %q, %v; want unavailable", sess.Status, err)
	}
}

// Requesting a pairing code records a run and applies it before asking, so the
// code is requested on the pairing connection of the recorded generation.
func TestSessionPairingCodeRecordsRunFirst(t *testing.T) {
	svc, facade, state, _ := lifecycleService(t, sessionReadRow(nil, domain.DesiredStop, 4, domain.SessionStopped, nil, true))
	facade.code = "ABCD1234"
	code, err := svc.PairingCode(context.Background(), "org_1", "sess_1", "6281234")
	if err != nil || code != "ABCD1234" {
		t.Fatalf("code = %q, %v", code, err)
	}
	if len(state.desired) != 1 || state.desired[0] != (desiredWrite{state: domain.DesiredRun, newStart: true}) {
		t.Fatalf("desired = %+v", state.desired)
	}
	if len(facade.calls) != 2 || facade.calls[0] != "apply" || facade.calls[1] != "pairing-code" || facade.applied[0].generation != 5 {
		t.Fatalf("calls = %v applied = %+v", facade.calls, facade.applied)
	}
}

// Logout clears the identity and records stop only after WhatsApp confirmed
// the unlink.
func TestSessionLogoutClearsAfterConfirmedUnlink(t *testing.T) {
	svc, facade, state, _ := lifecycleService(t, sessionReadRow("628@s.whatsapp.net", domain.DesiredRun, 4, domain.SessionWorking, nil, true))
	facade.err = errors.New("unlink not confirmed")
	if err := svc.Logout(context.Background(), "org_1", "sess_1"); err == nil {
		t.Fatal("logout succeeded although the unlink failed")
	}
	if len(state.cleared) != 0 {
		t.Fatal("a failed unlink cleared the pairing")
	}
}

func TestSessionServiceLifecycleWithoutStateStoreReturnsUnavailable(t *testing.T) {
	for name, action := range map[string]func(*SessionService) error{
		"start":   func(s *SessionService) error { return s.Start(context.Background(), "org_1", "sess_1") },
		"stop":    func(s *SessionService) error { return s.Stop(context.Background(), "org_1", "sess_1") },
		"restart": func(s *SessionService) error { return s.Restart(context.Background(), "org_1", "sess_1") },
	} {
		st, mock := newStore(t)
		mock.ExpectQuery("FROM wa_sessions").WithArgs("sess_1").WillReturnRows(sessionReadRow(nil, domain.DesiredStop, 0, domain.SessionStopped, nil, true))
		if err := action(NewSessionService(st.Sessions, nil, nil)); !isLiveUnavailable(err) {
			t.Fatalf("%s error = %v, want live unavailable", name, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}
