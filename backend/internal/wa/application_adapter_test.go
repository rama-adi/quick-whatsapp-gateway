package wa

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

var errFakeLiveOp = errors.New("live operation failed")

type fakeSessionStateSource struct {
	status              domain.SessionStatus
	connected, loggedIn bool
	found               bool
}

func (f fakeSessionStateSource) ConnectionState(string) (domain.SessionStatus, bool, bool, bool) {
	return f.status, f.connected, f.loggedIn, f.found
}

type fakeEngineLiveOps struct {
	ctx                               context.Context
	sessionID, state, chatJID, sender string
	messageIDs                        []string
	readAt                            time.Time
	err                               error
	calls                             int

	// group/contact/presence/backfill recording
	blockedJID        string
	blocked           bool
	groupInfo         domain.GroupInfo
	settings          domain.GroupSettings
	partAction        domain.GroupParticipantAction
	participants      []string
	inviteReset       bool
	joinedCode        string
	leftGroup         string
	chatPresenceState string
	subscribedChat    string
	backfillCalled    bool
}

func (f *fakeEngineLiveOps) SetPresence(ctx context.Context, sessionID, state string) error {
	f.calls++
	f.ctx, f.sessionID, f.state = ctx, sessionID, state
	return f.err
}

func (f *fakeEngineLiveOps) SendReadReceiptAt(ctx context.Context, sessionID, chatJID, sender string, ids []string, readAt time.Time) error {
	f.calls++
	f.ctx, f.sessionID, f.chatJID, f.sender = ctx, sessionID, chatJID, sender
	f.messageIDs, f.readAt = ids, readAt
	return f.err
}

func (f *fakeEngineLiveOps) IsOnWhatsApp(ctx context.Context, sessionID string, phones []string) ([]domain.OnWhatsApp, error) {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	return nil, f.err
}

func (f *fakeEngineLiveOps) ProfilePicture(ctx context.Context, sessionID, jid string) (domain.ProfilePicture, error) {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	return domain.ProfilePicture{}, f.err
}

func (f *fakeEngineLiveOps) About(ctx context.Context, sessionID, jid string) (string, error) {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	return "", f.err
}

func (f *fakeEngineLiveOps) SetBlocked(ctx context.Context, sessionID, jid string, blocked bool) error {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	f.blockedJID, f.blocked = jid, blocked
	return f.err
}

func (f *fakeEngineLiveOps) CreateGroup(ctx context.Context, sessionID, name string, participants []string) (domain.GroupInfo, error) {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	if f.err != nil {
		return domain.GroupInfo{}, f.err
	}
	f.groupInfo = domain.GroupInfo{GroupJID: "120363@g.us", Subject: name}
	return f.groupInfo, nil
}

func (f *fakeEngineLiveOps) UpdateParticipants(ctx context.Context, sessionID, groupJID string, participants []string, action domain.GroupParticipantAction) error {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	f.participants, f.partAction = participants, action
	return f.err
}

func (f *fakeEngineLiveOps) UpdateSettings(ctx context.Context, sessionID, groupJID string, s domain.GroupSettings) error {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	f.settings = s
	return f.err
}

func (f *fakeEngineLiveOps) GetInviteLink(ctx context.Context, sessionID, groupJID string, reset bool) (string, error) {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	f.inviteReset = reset
	return "", f.err
}

func (f *fakeEngineLiveOps) JoinWithLink(ctx context.Context, sessionID, code string) (string, error) {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	f.joinedCode = code
	return "", f.err
}

func (f *fakeEngineLiveOps) Leave(ctx context.Context, sessionID, groupJID string) error {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	f.leftGroup = groupJID
	return f.err
}

func (f *fakeEngineLiveOps) GetPresence(ctx context.Context, sessionID, chatJID string) (domain.PresenceStatus, error) {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	f.subscribedChat = chatJID
	return domain.PresenceStatus{}, f.err
}

func (f *fakeEngineLiveOps) SetChatPresence(ctx context.Context, sessionID, chatJID, state string) error {
	f.calls++
	f.ctx, f.sessionID = ctx, sessionID
	f.chatPresenceState = state
	return f.err
}

func (f *fakeEngineLiveOps) BackfillSessionData(ctx context.Context, sessionID string) (domain.BackfillSnapshot, error) {
	f.calls++
	f.backfillCalled = true
	f.ctx, f.sessionID = ctx, sessionID
	return domain.BackfillSnapshot{}, f.err
}

var adapterNow = time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC)

func testApplicationAdapter(live engineLiveOps) *ApplicationGatewayAdapter {
	return &ApplicationGatewayAdapter{
		gatewayID: "gateway-1",
		sessions: fakeSessionStateSource{
			status: domain.SessionWorking, connected: true, loggedIn: true, found: true,
		},
		live: live, now: func() time.Time { return adapterNow }, maxFutureSkew: DefaultReadReceiptFutureSkew,
	}
}

type fakeAssignmentFence struct{ owns, allows bool }

func (f fakeAssignmentFence) AllowsMutation(string, string, uint64) bool { return f.allows }
func (f fakeAssignmentFence) OwnsSession(string, string, uint64) bool    { return f.owns }
func (f fakeAssignmentFence) OwnsAssignment(string, string, uint64) bool { return f.owns }

func TestApplicationGatewayAdapterRejectsStaleAssignmentBeforeLiveOperation(t *testing.T) {
	live := &fakeEngineLiveOps{}
	a := testApplicationAdapter(live)
	a.fence = fakeAssignmentFence{owns: true, allows: false}
	_, err := a.SetAccountPresence(context.Background(), application.SetPresenceCommand{CommandID: "c", OrganizationID: "o", SessionID: "s", GatewayID: "gateway-1", AssignmentEpoch: 1, State: application.AccountPresenceOnline})
	assertAPIError(t, err, domain.CodeConflict, "session assignment epoch is stale or lease expired")
	if live.calls != 0 {
		t.Fatalf("stale mutation reached live engine")
	}
}

func assertAPIError(t *testing.T, err error, code, message string) {
	t.Helper()
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *domain.APIError", err)
	}
	if apiErr.Code != code || apiErr.Message != message {
		t.Fatalf("error = (%q, %q), want (%q, %q)", apiErr.Code, apiErr.Message, code, message)
	}
}

type fakeDispatcher struct {
	calls       int
	waMessageID string
	ts          int64
	err         error
	lastSession string
}

func (f *fakeDispatcher) Dispatch(_ context.Context, req domain.SendRequest) (string, int64, error) {
	f.calls++
	f.lastSession = req.To
	return f.waMessageID, f.ts, f.err
}

type fakeLedger struct {
	stored map[string]application.CommandResultRecord
	saved  []application.CommandResultRecord
	err    error
}

func (f *fakeLedger) LookupCommand(_ context.Context, commandID string) (*application.CommandResultRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	if record, ok := f.stored[commandID]; ok {
		return &record, nil
	}
	return nil, nil
}

func (f *fakeLedger) SaveCommandResult(_ context.Context, record application.CommandResultRecord) error {
	if f.err != nil {
		return f.err
	}
	f.saved = append(f.saved, record)
	if f.stored == nil {
		f.stored = map[string]application.CommandResultRecord{}
	}
	if _, exists := f.stored[record.CommandID]; !exists {
		f.stored[record.CommandID] = record
	}
	return nil
}

func sendTestAdapter(dispatch sendDispatcher, ledger commandLedger, allows bool) (*ApplicationGatewayAdapter, *fakeEngineLiveOps) {
	live := &fakeEngineLiveOps{}
	adapter := testApplicationAdapter(live)
	adapter.dispatch = dispatch
	adapter.ledger = ledger
	adapter.fence = fakeAssignmentFence{owns: true, allows: allows}
	adapter.inFlight = map[string]*commandFlight{}
	return adapter, live
}

func textSendCommand(commandID string) application.SendCommand {
	return application.SendCommand{
		CommandID: commandID, OrganizationID: "org_1", SessionID: "ses_1",
		GatewayID: "gateway-1", AssignmentEpoch: 4,
		Payload: domain.SendRequest{Type: domain.SendTypeText, To: "628123@s.whatsapp.net", Text: "hi"},
	}
}

func TestSendMessageValidationFailureIsTerminalButTransientFailureIsNot(t *testing.T) {
	validation := &fakeDispatcher{err: domain.ErrValidation("bad payload")}
	validationLedger := &fakeLedger{}
	adapter, _ := sendTestAdapter(validation, validationLedger, true)
	if _, err := adapter.SendMessage(context.Background(), textSendCommand("cmd_bad")); err == nil {
		t.Fatal("want validation error")
	}
	if len(validationLedger.saved) != 1 || validationLedger.saved[0].Status != application.CommandFailed {
		t.Fatalf("validation not recorded: %#v", validationLedger.saved)
	}

	transient := &fakeDispatcher{err: errors.New("whatsapp disconnected")}
	transientLedger := &fakeLedger{}
	adapter, _ = sendTestAdapter(transient, transientLedger, true)
	if _, err := adapter.SendMessage(context.Background(), textSendCommand("cmd_transient")); err == nil {
		t.Fatal("want transient error")
	}
	if len(transientLedger.saved) != 0 {
		t.Fatalf("transient failure was recorded as terminal: %#v", transientLedger.saved)
	}
}

// TestSendMessageFailsClosedWithoutLedger ensures a miscomposed gateway cannot
// silently execute non-idempotent sends.
func TestSendMessageFailsClosedWithoutLedger(t *testing.T) {
	dispatch := &fakeDispatcher{waMessageID: "WA_1"}
	adapter, _ := sendTestAdapter(dispatch, nil, true)
	if _, err := adapter.SendMessage(context.Background(), textSendCommand("cmd_1")); err == nil {
		t.Fatal("expected error without ledger")
	}
	if dispatch.calls != 0 {
		t.Fatal("dispatch ran without a ledger")
	}
}

type blockedDispatcher struct {
	inner *fakeDispatcher
	gate  chan struct{}
}

func (b *blockedDispatcher) Dispatch(ctx context.Context, req domain.SendRequest) (string, int64, error) {
	<-b.gate
	return b.inner.Dispatch(ctx, req)
}

// ---- Increment 7 live resource slices ----

type fakeController struct {
	devicesEnsured  [][2]string // {id, organizationID}
	qrStarted       []string
	pairingRequests [][2]string // {id, phone}
	logouts         []string
	forgotten       []string
	latestQR        map[string]application.PairingSnapshot
	err             error
}

func (f *fakeController) EnsureDevice(id, organizationID string) {
	f.devicesEnsured = append(f.devicesEnsured, [2]string{id, organizationID})
}

func (f *fakeController) StartQR(_ context.Context, id string) error {
	f.qrStarted = append(f.qrStarted, id)
	// Mirror the real manager: once QR pairing starts, a code becomes available.
	if f.latestQR == nil {
		f.latestQR = map[string]application.PairingSnapshot{}
	}
	if f.latestQR[id].Code == "" {
		f.latestQR[id] = application.PairingSnapshot{Code: "QR_FRESH", ExpiresAt: 777}
	}
	return f.err
}

func (f *fakeController) StartPairingCode(_ context.Context, id, phone string) (string, error) {
	f.pairingRequests = append(f.pairingRequests, [2]string{id, phone})
	return "PAIR-CODE", f.err
}

func (f *fakeController) Logout(_ context.Context, id string) error {
	f.logouts = append(f.logouts, id)
	return f.err
}

func (f *fakeController) Forget(id string) {
	f.forgotten = append(f.forgotten, id)
}

func (f *fakeController) LatestQR(id string) (string, int64) {
	snap := f.latestQR[id]
	return snap.Code, snap.ExpiresAt
}

func lifecycleTestAdapter(controller *fakeController, ledger commandLedger, allows bool) *ApplicationGatewayAdapter {
	adapter := testApplicationAdapter(&fakeEngineLiveOps{})
	adapter.controller = controller
	adapter.ledger = ledger
	adapter.fence = fakeAssignmentFence{owns: true, allows: allows}
	adapter.inFlight = map[string]*commandFlight{}
	return adapter
}

func isValidation(err error) bool {
	var apiErr *domain.APIError
	return errors.As(err, &apiErr) && apiErr.Code == domain.CodeValidationError
}

func isConflict(err error) bool {
	var apiErr *domain.APIError
	return errors.As(err, &apiErr) && apiErr.Code == domain.CodeConflict
}

func blockCommand(commandID string) application.ContactJIDCommand {
	return application.ContactJIDCommand{
		CommandID: commandID, OrganizationID: "org_1", SessionID: "ses_1",
		GatewayID: "gateway-1", AssignmentEpoch: 4, JID: "628123@s.whatsapp.net", Blocked: true,
	}
}

func groupCommand(kind application.GroupMutationKind, commandID string) application.GroupMutationCommand {
	command := application.GroupMutationCommand{
		CommandID: commandID, OrganizationID: "org_1", SessionID: "ses_1",
		GatewayID: "gateway-1", AssignmentEpoch: 4,
	}
	switch kind {
	case application.GroupOpCreate:
		command.Kind, command.Name, command.Participants = kind, "Team", []string{"628123@s.whatsapp.net"}
	case application.GroupOpUpdateSettings:
		subject := "NewName"
		command.Kind, command.GroupJID, command.Settings = kind, "120363@g.us", application.GroupSettingsUpdate{Subject: &subject}
	case application.GroupOpUpdateParticipants:
		command.Kind, command.GroupJID = kind, "120363@g.us"
		command.Participants, command.Action = []string{"628123@s.whatsapp.net"}, application.GroupChangePromote
	default:
		command.Kind, command.GroupJID = kind, "120363@g.us"
	}
	return command
}

func TestMutateGroupValidationFailureIsTerminalButTransientFailureIsNot(t *testing.T) {
	invalid := groupCommand(application.GroupOpUpdateParticipants, "grp_bad")
	invalid.Action = "bogus"
	live := &fakeEngineLiveOps{}
	ledger := &fakeLedger{}
	adapter, _ := sendTestAdapter(nil, ledger, true)
	adapter.live = live
	if _, err := adapter.MutateGroup(context.Background(), invalid); err == nil {
		t.Fatal("want validation error")
	}
	if len(ledger.saved) != 1 || ledger.saved[0].Status != application.CommandFailed {
		t.Fatalf("validation not recorded: %#v", ledger.saved)
	}

	transient := groupCommand(application.GroupOpLeave, "grp_transient")
	live2 := &fakeEngineLiveOps{err: errors.New("whatsapp disconnected")}
	ledger2 := &fakeLedger{}
	adapter2, _ := sendTestAdapter(nil, ledger2, true)
	adapter2.live = live2
	if _, err := adapter2.MutateGroup(context.Background(), transient); err == nil {
		t.Fatal("want transient error")
	}
	if len(ledger2.saved) != 0 {
		t.Fatalf("transient failure was recorded as terminal: %#v", ledger2.saved)
	}
}
