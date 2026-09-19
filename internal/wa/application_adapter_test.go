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

func TestApplicationGatewayAdapterSessionState(t *testing.T) {
	a := testApplicationAdapter(&fakeEngineLiveOps{})
	got, err := a.GetSessionState(context.Background(), application.SessionStateQuery{
		OrganizationID: "org-1", SessionID: "session-1", GatewayID: "gateway-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.OrganizationID != "org-1" || got.SessionID != "session-1" || got.GatewayID != "gateway-1" || got.Status != domain.SessionWorking || !got.Connected || !got.LoggedIn {
		t.Fatalf("unexpected state: %#v", got)
	}
}

func TestApplicationGatewayAdapterPreservesContextAndMetadata(t *testing.T) {
	live := &fakeEngineLiveOps{}
	a := testApplicationAdapter(live)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	got, err := a.SetAccountPresence(ctx, application.SetPresenceCommand{
		CommandID: "command-1", OrganizationID: "org-1", SessionID: "session-1",
		GatewayID: "gateway-1", AssignmentEpoch: 7, State: application.AccountPresenceOnline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if live.ctx != ctx || live.sessionID != "session-1" || live.state != "online" || live.calls != 1 {
		t.Fatalf("operation was not delegated faithfully: %#v", live)
	}
	if got.CommandID != "command-1" || got.AssignmentEpoch != 7 || got.OrganizationID != "org-1" || got.SessionID != "session-1" || got.GatewayID != "gateway-1" {
		t.Fatalf("unexpected result metadata: %#v", got)
	}
}

func TestApplicationGatewayAdapterReadReceiptUsesStableTimestamp(t *testing.T) {
	live := &fakeEngineLiveOps{}
	a := testApplicationAdapter(live)
	readAt := adapterNow.Add(-24 * time.Hour) // Old timestamps are valid; only future skew is bounded.
	ctx := context.Background()

	_, err := a.MarkRead(ctx, application.MarkReadCommand{
		CommandID: "command-2", OrganizationID: "org-1", SessionID: "session-1",
		GatewayID: "gateway-1", AssignmentEpoch: 8, ChatJID: "123@g.us",
		SenderJID: "456@s.whatsapp.net", MessageIDs: []string{"message-1"}, ReadAt: readAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if live.ctx != ctx || live.chatJID != "123@g.us" || live.sender != "456@s.whatsapp.net" || len(live.messageIDs) != 1 || live.messageIDs[0] != "message-1" || !live.readAt.Equal(readAt) || live.calls != 1 {
		t.Fatalf("read receipt was not delegated faithfully: %#v", live)
	}
}

func TestApplicationGatewayAdapterValidationDoesNotDelegate(t *testing.T) {
	validPresence := application.SetPresenceCommand{CommandID: "c", OrganizationID: "o", SessionID: "s", GatewayID: "gateway-1", AssignmentEpoch: 1, State: application.AccountPresenceOnline}
	validRead := application.MarkReadCommand{CommandID: "c", OrganizationID: "o", SessionID: "s", GatewayID: "gateway-1", AssignmentEpoch: 1, ChatJID: "1@g.us", SenderJID: "2@s.whatsapp.net", MessageIDs: []string{"m"}, ReadAt: adapterNow}
	tests := []struct {
		name, message string
		call          func(*ApplicationGatewayAdapter) error
	}{
		{"missing command", "command_id is required", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.CommandID = ""
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"missing organization", "organization_id, session_id, and gateway_id are required", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.OrganizationID = ""
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"missing session", "organization_id, session_id, and gateway_id are required", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.SessionID = ""
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"missing gateway", "organization_id, session_id, and gateway_id are required", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.GatewayID = ""
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"zero presence epoch", "assignment_epoch must be at least 1", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.AssignmentEpoch = 0
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"unknown presence", "presence state must be online or offline", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.State = "away"
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"zero read epoch", "assignment_epoch must be at least 1", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.AssignmentEpoch = 0
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"empty IDs", "chat_jid, message_ids, and read_at are required", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.MessageIDs = nil
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"all-empty IDs", "message_ids must not contain empty values", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.MessageIDs = []string{"", ""}
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"zero timestamp", "chat_jid, message_ids, and read_at are required", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.ReadAt = time.Time{}
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"future timestamp", "read_at exceeds allowed future clock skew", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.ReadAt = adapterNow.Add(DefaultReadReceiptFutureSkew + time.Nanosecond)
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"invalid chat JID", "chat_jid is invalid", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.ChatJID = "a@b@c"
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"invalid sender JID", "sender_jid is invalid", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.SenderJID = "a@b@c"
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"group missing sender JID", "sender_jid is required for group read receipts", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.SenderJID = ""
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live := &fakeEngineLiveOps{}
			err := tt.call(testApplicationAdapter(live))
			assertAPIError(t, err, domain.CodeValidationError, tt.message)
			if live.calls != 0 {
				t.Fatalf("validation failure delegated %d live calls", live.calls)
			}
		})
	}
}

func TestApplicationGatewayAdapterExactTargetAndLiveErrors(t *testing.T) {
	live := &fakeEngineLiveOps{err: errFakeLiveOp}
	a := testApplicationAdapter(live)
	_, err := a.GetSessionState(context.Background(), application.SessionStateQuery{OrganizationID: "o", SessionID: "s", GatewayID: "other"})
	assertAPIError(t, err, domain.CodeNotFound, "gateway target does not match this gateway")

	_, err = a.SetAccountPresence(context.Background(), application.SetPresenceCommand{CommandID: "c", OrganizationID: "o", SessionID: "s", GatewayID: "gateway-1", AssignmentEpoch: 1, State: application.AccountPresenceOnline})
	if !errors.Is(err, errFakeLiveOp) {
		t.Fatalf("error = %v, want live error", err)
	}
	if live.calls != 1 {
		t.Fatalf("live calls = %d, want 1", live.calls)
	}

	live.err = errFakeLiveOp
	_, err = a.MarkRead(context.Background(), application.MarkReadCommand{
		CommandID: "c2", OrganizationID: "o", SessionID: "s", GatewayID: "gateway-1",
		AssignmentEpoch: 1, ChatJID: "1@g.us", SenderJID: "2@s.whatsapp.net", MessageIDs: []string{"m"}, ReadAt: adapterNow,
	})
	if !errors.Is(err, errFakeLiveOp) {
		t.Fatalf("read error = %v, want live error", err)
	}
	if live.calls != 2 {
		t.Fatalf("live calls = %d, want 2", live.calls)
	}
}

func TestApplicationGatewayAdapterSessionNotFoundError(t *testing.T) {
	a := testApplicationAdapter(&fakeEngineLiveOps{})
	a.sessions = fakeSessionStateSource{}
	_, err := a.GetSessionState(context.Background(), application.SessionStateQuery{OrganizationID: "o", SessionID: "missing", GatewayID: "gateway-1"})
	assertAPIError(t, err, domain.CodeNotFound, "session not found")
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

// TestSendMessageExecutesAndRecordsBeforeResponding pins the write-ahead
// ledger contract: a successful send is durably recorded with its WhatsApp id.
func TestSendMessageExecutesAndRecordsBeforeResponding(t *testing.T) {
	dispatch := &fakeDispatcher{waMessageID: "WA_1", ts: 5000}
	ledger := &fakeLedger{}
	adapter, _ := sendTestAdapter(dispatch, ledger, true)

	result, err := adapter.SendMessage(context.Background(), textSendCommand("cmd_1"))
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.WAMessageID != "WA_1" || !result.SentAt.Equal(time.UnixMilli(5000).UTC()) || result.AssignmentEpoch != 4 {
		t.Fatalf("result = %#v", result)
	}
	if len(ledger.saved) != 1 || ledger.saved[0].Status != application.CommandSent || ledger.saved[0].WAMessageID != "WA_1" {
		t.Fatalf("saved = %#v", ledger.saved)
	}
	if dispatch.calls != 1 {
		t.Fatalf("dispatch calls = %d", dispatch.calls)
	}
}

// TestSendMessageReplaysStoredResultWithoutRedispatch pins the Increment 6 exit
// criterion: a repeated command_id after a lost response returns the original
// result and never sends twice.
func TestSendMessageReplaysStoredResultWithoutRedispatch(t *testing.T) {
	dispatch := &fakeDispatcher{waMessageID: "WA_1"}
	ledger := &fakeLedger{stored: map[string]application.CommandResultRecord{
		"cmd_1": {CommandID: "cmd_1", SessionID: "ses_1", Status: application.CommandSent, WAMessageID: "WA_ORIGINAL", UpdatedAt: time.UnixMilli(42).UTC()},
	}}
	adapter, _ := sendTestAdapter(dispatch, ledger, false)

	result, err := adapter.SendMessage(context.Background(), textSendCommand("cmd_1"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.WAMessageID != "WA_ORIGINAL" || !result.SentAt.Equal(time.UnixMilli(42).UTC()) {
		t.Fatalf("replayed result = %#v", result)
	}
	if dispatch.calls != 0 {
		t.Fatalf("replay re-dispatched %d times", dispatch.calls)
	}
}

// TestSendMessageValidationFailureIsTerminalButTransientFailureIsNot pins the
// outcome classification: deterministic rejections are recorded so retries fail
// identically, while transient errors leave the command retryable.
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

// TestSendMessageConcurrentDuplicateJoinsFirstExecution verifies the in-process
// singleflight: two identical concurrent commands produce one dispatch and two
// identical results (the follower waits for the leader's ledger write).
func TestSendMessageConcurrentDuplicateJoinsFirstExecution(t *testing.T) {
	release := make(chan struct{})
	dispatch := &fakeDispatcher{waMessageID: "WA_RACE"}
	ledger := &fakeLedger{}
	adapter, _ := sendTestAdapter(&blockedDispatcher{inner: dispatch, gate: release}, ledger, true)

	type call struct {
		result application.SendMessageResult
		err    error
	}
	results := make(chan call, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, err := adapter.SendMessage(context.Background(), textSendCommand("cmd_race"))
			results <- call{result, err}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("errors: %v %v", first.err, second.err)
	}
	if first.result.WAMessageID != "WA_RACE" || second.result.WAMessageID != "WA_RACE" {
		t.Fatalf("results diverged: %#v %#v", first.result, second.result)
	}
	if dispatch.calls != 1 {
		t.Fatalf("concurrent duplicates dispatched %d times", dispatch.calls)
	}
	if len(ledger.saved) != 1 {
		t.Fatalf("ledger writes = %d", len(ledger.saved))
	}
}

// blockedDispatcher holds every Dispatch call until the test releases it,
// exercising the duplicate command's wait path.
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

// TestPrepareSessionRegistersDeviceIdempotently pins the pairing-substrate
// contract: a first prepare registers the device + managed-session entry, and
// a repeated prepare is a success that never re-creates it. No ledger is
// involved and no epoch fence applies beyond target validation.
func TestPrepareSessionRegistersDeviceIdempotently(t *testing.T) {
	controller := &fakeController{}
	adapter := lifecycleTestAdapter(controller, nil, false)
	query := application.SessionStateQuery{OrganizationID: "org_1", SessionID: "ses_1", GatewayID: "gateway-1", AssignmentEpoch: 2}

	result, err := adapter.PrepareSession(context.Background(), query)
	if err != nil {
		t.Fatalf("PrepareSession: %v", err)
	}
	if len(controller.devicesEnsured) != 1 || controller.devicesEnsured[0] != [2]string{"ses_1", "org_1"} {
		t.Fatalf("first ensure = %v", controller.devicesEnsured)
	}
	if _, err := adapter.PrepareSession(context.Background(), query); err != nil {
		t.Fatalf("repeat PrepareSession: %v", err)
	}
	if len(controller.devicesEnsured) != 2 {
		t.Fatalf("re-prepare re-ran EnsureDevice %d times (the manager itself is the idempotence)", len(controller.devicesEnsured))
	}
	if result.AssignmentEpoch != 2 || result.SessionID != "ses_1" || result.GatewayID != "gateway-1" {
		t.Fatalf("result = %#v", result)
	}
}

// TestBeginPairingReturnsSnapshotThenKicksPairing pins the QR read shape: an
// existing code is returned without touching the runtime; with no code ready,
// StartQR runs and the post-kick snapshot is returned.
func TestBeginPairingReturnsSnapshotThenKicksPairing(t *testing.T) {
	controller := &fakeController{latestQR: map[string]application.PairingSnapshot{
		"ses_1": {Code: "QR_LIVE", ExpiresAt: 555},
	}}
	adapter := lifecycleTestAdapter(controller, nil, true)
	query := application.SessionStateQuery{OrganizationID: "org_1", SessionID: "ses_1", GatewayID: "gateway-1", AssignmentEpoch: 3}

	snapshot, err := adapter.BeginPairing(context.Background(), query)
	if err != nil || snapshot.Code != "QR_LIVE" || snapshot.ExpiresAt != 555 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	if len(controller.qrStarted) != 0 {
		t.Fatal("existing code triggered a redundant StartQR")
	}

	// With no code ready, StartQR runs and the post-kick snapshot is returned
	// (the fake populates a fresh code inside StartQR, mirroring the manager's
	// async first-code arrival).
	controller.latestQR = map[string]application.PairingSnapshot{}
	snapshot, err = adapter.BeginPairing(context.Background(), query)
	if err != nil || snapshot.Code != "QR_FRESH" || snapshot.ExpiresAt != 777 {
		t.Fatalf("post-kick snapshot = %#v, %v", snapshot, err)
	}
	if len(controller.qrStarted) != 1 || controller.qrStarted[0] != "ses_1" {
		t.Fatalf("StartQR calls = %v", controller.qrStarted)
	}
}

// TestPairPhoneCarriesPhoneBehindFence pins the phone-pairing read path.
func TestPairPhoneCarriesPhoneBehindFence(t *testing.T) {
	controller := &fakeController{}
	adapter := lifecycleTestAdapter(controller, nil, true)
	query := application.SessionStateQuery{OrganizationID: "org_1", SessionID: "ses_1", GatewayID: "gateway-1", AssignmentEpoch: 3}

	code, err := adapter.PairPhone(context.Background(), query, "+628123")
	if err != nil || code != "PAIR-CODE" {
		t.Fatalf("PairPhone = %q, %v", code, err)
	}
	if len(controller.pairingRequests) != 1 || controller.pairingRequests[0][1] != "+628123" {
		t.Fatalf("requests = %v", controller.pairingRequests)
	}

	if _, err := adapter.PairPhone(context.Background(), query, ""); !isValidation(err) {
		t.Fatal("empty phone accepted")
	}
	stale := lifecycleTestAdapter(controller, nil, false)
	stale.fence = fakeAssignmentFence{owns: false, allows: false}
	if _, err := stale.PairPhone(context.Background(), query, "+628123"); !isConflict(err) {
		t.Fatal("stale fence accepted")
	}
}

func isValidation(err error) bool {
	var apiErr *domain.APIError
	return errors.As(err, &apiErr) && apiErr.Code == domain.CodeValidationError
}

func isConflict(err error) bool {
	var apiErr *domain.APIError
	return errors.As(err, &apiErr) && apiErr.Code == domain.CodeConflict
}

// TestLogoutSessionRecordsSentWithoutWAMessageID pins that logout is
// ledger-backed like sends but records CommandSent only.
func TestLogoutSessionRecordsSentWithoutWAMessageID(t *testing.T) {
	controller := &fakeController{}
	ledger := &fakeLedger{}
	adapter := lifecycleTestAdapter(controller, ledger, true)

	command := blockCommand("out_1") // same routing shape as other mutations
	result, err := adapter.LogoutSession(context.Background(), command)
	if err != nil {
		t.Fatalf("LogoutSession: %v", err)
	}
	if result.CommandID != "out_1" || result.AssignmentEpoch != 4 {
		t.Fatalf("result = %#v", result)
	}
	if len(ledger.saved) != 1 || ledger.saved[0].Status != application.CommandSent || ledger.saved[0].WAMessageID != "" {
		t.Fatalf("saved = %#v", ledger.saved)
	}
	if len(controller.logouts) != 1 || controller.logouts[0] != "ses_1" {
		t.Fatalf("logouts = %v", controller.logouts)
	}
}

// TestLogoutSessionReplaysStoredResultWithoutRelinking mirrors the SetBlocked
// replay test: a repeated command id never re-executes, and a stale fence does
// not block a recorded outcome.
func TestLogoutSessionReplaysStoredResultWithoutRelinking(t *testing.T) {
	controller := &fakeController{}
	ledger := &fakeLedger{stored: map[string]application.CommandResultRecord{
		"out_1": {CommandID: "out_1", SessionID: "ses_1", Status: application.CommandSent, UpdatedAt: time.UnixMilli(42).UTC()},
	}}
	adapter := lifecycleTestAdapter(controller, ledger, false)

	result, err := adapter.LogoutSession(context.Background(), blockCommand("out_1"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.CommandID != "out_1" {
		t.Fatalf("replayed = %#v", result)
	}
	if len(controller.logouts) != 0 {
		t.Fatalf("replay re-executed %d times", len(controller.logouts))
	}
}

// TestForgetSessionDropsRuntimeWithoutFenceOrLedger pins forget's contract:
// only the target validates (no epoch — the row may be deleted API-side), and
// the in-memory drop always runs. Idempotent by construction.
func TestForgetSessionDropsRuntimeWithoutFenceOrLedger(t *testing.T) {
	controller := &fakeController{}
	adapter := lifecycleTestAdapter(controller, nil, false) // fence would deny any epoch check

	if err := adapter.ForgetSession(context.Background(), "org_1", "ses_1"); err != nil {
		t.Fatalf("ForgetSession: %v", err)
	}
	if err := adapter.ForgetSession(context.Background(), "org_1", "ses_1"); err != nil {
		t.Fatalf("repeat ForgetSession: %v", err)
	}
	if len(controller.forgotten) != 2 || controller.forgotten[0] != "ses_1" {
		t.Fatalf("forgotten = %v", controller.forgotten)
	}
	if _, err := adapter.PrepareSession(context.Background(), application.SessionStateQuery{}); err == nil {
		t.Fatal("empty target accepted")
	}
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

// TestSetBlockedRecordsSentWithoutWAMessageID pins that the blocklist mutation
// is ledger-backed like sends but records CommandSent only (no wa message id).
func TestSetBlockedRecordsSentWithoutWAMessageID(t *testing.T) {
	live := &fakeEngineLiveOps{}
	ledger := &fakeLedger{}
	adapter, _ := sendTestAdapter(nil, ledger, true)
	adapter.live = live

	result, err := adapter.SetBlocked(context.Background(), blockCommand("blk_1"))
	if err != nil {
		t.Fatalf("SetBlocked: %v", err)
	}
	if result.CommandID != "blk_1" || result.AssignmentEpoch != 4 {
		t.Fatalf("result = %#v", result)
	}
	if len(ledger.saved) != 1 || ledger.saved[0].Status != application.CommandSent || ledger.saved[0].WAMessageID != "" {
		t.Fatalf("saved = %#v", ledger.saved)
	}
	if live.blockedJID != "628123@s.whatsapp.net" || !live.blocked {
		t.Fatalf("live call = jid %q blocked %v", live.blockedJID, live.blocked)
	}
}

// TestSetBlockedReplaysStoredResultWithoutReblocking pins replay semantics for
// the blocklist mutation: a repeated command id never re-executes, and a stale
// fence does not block a recorded outcome.
func TestSetBlockedReplaysStoredResultWithoutReblocking(t *testing.T) {
	live := &fakeEngineLiveOps{}
	ledger := &fakeLedger{stored: map[string]application.CommandResultRecord{
		"blk_1": {CommandID: "blk_1", SessionID: "ses_1", Status: application.CommandSent, UpdatedAt: time.UnixMilli(42).UTC()},
	}}
	adapter, _ := sendTestAdapter(nil, ledger, false)
	adapter.live = live

	result, err := adapter.SetBlocked(context.Background(), blockCommand("blk_1"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.CommandID != "blk_1" {
		t.Fatalf("replayed = %#v", result)
	}
	if live.calls != 0 {
		t.Fatalf("replay re-executed %d times", live.calls)
	}
}

// TestMutateGroupCreateCarriesGroupMetadataAndLedger pins create-group's
// write-ahead record plus raw-metadata passthrough.
func TestMutateGroupCreateCarriesGroupMetadataAndLedger(t *testing.T) {
	live := &fakeEngineLiveOps{}
	ledger := &fakeLedger{}
	adapter, _ := sendTestAdapter(nil, ledger, true)
	adapter.live = live

	result, err := adapter.MutateGroup(context.Background(), groupCommand(application.GroupOpCreate, "grp_1"))
	if err != nil {
		t.Fatalf("MutateGroup: %v", err)
	}
	if result.CreatedGroup.GroupJID != "120363@g.us" || result.CreatedGroup.Subject != "Team" {
		t.Fatalf("created = %#v", result.CreatedGroup)
	}
	if len(ledger.saved) != 1 || ledger.saved[0].Status != application.CommandSent || ledger.saved[0].WAMessageID != "120363@g.us" {
		t.Fatalf("saved = %#v", ledger.saved)
	}
}

// TestMutateGroupReplaysStoredCreateWithoutRedispatch pins that a repeated
// create-group command returns the stored group JID without touching WhatsApp.
func TestMutateGroupReplaysStoredCreateWithoutRedispatch(t *testing.T) {
	live := &fakeEngineLiveOps{}
	ledger := &fakeLedger{stored: map[string]application.CommandResultRecord{
		"grp_1": {CommandID: "grp_1", SessionID: "ses_1", Status: application.CommandSent, WAMessageID: "120363@g.us", UpdatedAt: time.UnixMilli(42).UTC()},
	}}
	adapter, _ := sendTestAdapter(nil, ledger, false)
	adapter.live = live

	result, err := adapter.MutateGroup(context.Background(), groupCommand(application.GroupOpCreate, "grp_1"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.CreatedGroup.GroupJID != "120363@g.us" {
		t.Fatalf("replayed = %#v", result)
	}
	if live.calls != 0 {
		t.Fatalf("replay re-executed %d times", live.calls)
	}
}

// TestMutateGroupValidationFailureIsTerminalButTransientFailureIsNot mirrors
// the send pipeline's outcome classification for group commands.
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

// TestReadOperationsNeverTouchLedgerOrFence pins the read contract: contact
// lookups, picture/about, invite links, chat presence subscription, typing
// state, and backfill execute behind the ownership check only — no command id
// is required and no ledger row is written.
func TestReadOperationsNeverTouchLedgerOrFence(t *testing.T) {
	live := &fakeEngineLiveOps{}
	ledger := &fakeLedger{}
	adapter, _ := sendTestAdapter(nil, ledger, false) // stale fence: reads must still pass
	adapter.live = live
	ctx := context.Background()
	query := application.SessionStateQuery{OrganizationID: "org_1", SessionID: "ses_1", GatewayID: "gateway-1", AssignmentEpoch: 4}

	if _, err := adapter.LookupContact(ctx, application.LookupContactCommand{OrganizationID: query.OrganizationID, SessionID: query.SessionID, GatewayID: query.GatewayID, AssignmentEpoch: query.AssignmentEpoch, Phones: []string{"+62"}}); err != nil {
		t.Fatalf("LookupContact: %v", err)
	}
	if _, err := adapter.GetContactPicture(ctx, query, "j@s.whatsapp.net"); err != nil {
		t.Fatalf("GetContactPicture: %v", err)
	}
	if _, err := adapter.GetContactAbout(ctx, query, "j@s.whatsapp.net"); err != nil {
		t.Fatalf("GetContactAbout: %v", err)
	}
	if _, err := adapter.GetGroupInviteLink(ctx, query, "g@g.us", false); err != nil {
		t.Fatalf("GetGroupInviteLink: %v", err)
	}
	if _, err := adapter.JoinGroup(ctx, query, "code"); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}
	if _, err := adapter.GetChatPresence(ctx, query, "c@s.whatsapp.net"); err != nil {
		t.Fatalf("GetChatPresence: %v", err)
	}
	if err := adapter.SetChatPresence(ctx, application.ChatPresenceCommand{OrganizationID: query.OrganizationID, SessionID: query.SessionID, GatewayID: query.GatewayID, AssignmentEpoch: query.AssignmentEpoch, ChatJID: "c@s.whatsapp.net", State: "composing"}); err != nil {
		t.Fatalf("SetChatPresence: %v", err)
	}
	if _, err := adapter.BackfillSession(ctx, query); err != nil {
		t.Fatalf("BackfillSession: %v", err)
	}
	if len(ledger.saved) != 0 {
		t.Fatalf("reads wrote %d ledger rows", len(ledger.saved))
	}
}
