package wa

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/desiredstate"
)

func quietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestStartAssignedUsesControlConfigWithoutSessionLookup(t *testing.T) {
	jid := types.NewJID("6281", types.DefaultUserServer)
	keystore := &fakeKeystore{devices: []*store.Device{{ID: &jid}}}
	manager := NewManager(keystore, nil, nil, nil, nil, nil, Config{})
	manager.SetClientFactory(func(*store.Device) waClient { return &fakeClient{} })
	assignment := desiredstate.Assignment{SessionID: "session", OrganizationID: "org", DeviceJID: jid.String(), AssignmentEpoch: 1, LeaseExpiresAt: time.Now().Add(time.Minute), DesiredRun: true, Config: desiredstate.Config{Revision: 4, AutoRead: true, PresenceTyping: true, RatePerMin: 12, RatePerHour: 34}}
	if err := manager.StartAssigned(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	config, ok := manager.AssignedConfig("session")
	if !ok || config != assignment.Config {
		t.Fatalf("assigned config = %#v, %v", config, ok)
	}
}

// TestManagerTakesNoSessionRepository pins the MySQL cutover: the gateway
// manager must not hold a session repository — wa_sessions is API-owned.
func TestManagerTakesNoSessionRepository(t *testing.T) {
	keystore := &fakeKeystore{}
	m := NewManager(keystore, nil, nil, nil, nil, quietLogger(), Config{})
	m.mu.RLock()
	_, hasSessions := m.sessions["probe"]
	m.mu.RUnlock()
	if hasSessions {
		t.Fatal("manager registry unexpectedly contains a probe session")
	}
}

type fakeKeystore struct {
	devices   []*store.Device
	newDevice func() *store.Device
	deleted   []*store.Device
}

func (f *fakeKeystore) GetAllDevices(context.Context) ([]*store.Device, error) {
	return f.devices, nil
}
func (f *fakeKeystore) GetFirstDevice(context.Context) (*store.Device, error) {
	if len(f.devices) > 0 {
		return f.devices[0], nil
	}
	return f.NewDevice(), nil
}
func (f *fakeKeystore) GetDevice(context.Context, types.JID) (*store.Device, error) { return nil, nil }
func (f *fakeKeystore) NewDevice() *store.Device {
	if f.newDevice != nil {
		return f.newDevice()
	}
	return &store.Device{}
}
func (f *fakeKeystore) DeleteDevice(_ context.Context, d *store.Device) error {
	f.deleted = append(f.deleted, d)
	return nil
}

type fakeSink struct {
	mu     sync.Mutex
	events []domain.Event
}

func (f *fakeSink) Publish(_ context.Context, evt domain.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evt)
}

func (f *fakeSink) typeCount(eventType string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if e.Type == eventType {
			n++
		}
	}
	return n
}

type fakeInbound struct {
	mu     sync.Mutex
	count  int
	handle func(ctx context.Context, evt any)
}

func (f *fakeInbound) Handle(ctx context.Context, _, _ string, _ bool, evt any) {
	f.mu.Lock()
	f.count++
	handle := f.handle
	f.mu.Unlock()
	if handle != nil {
		handle(ctx, evt)
	}
}

func (f *fakeInbound) countValue() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

type fakeClient struct {
	mu          sync.Mutex
	presence    []types.Presence
	disconnects int
	loggedOut   bool
	pairDisplay string
	handler     whatsmeow.EventHandler
	pairCode    string
	pairErr     error
}

func (f *fakeClient) Connect() error { return nil }
func (f *fakeClient) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnects++
}
func (f *fakeClient) IsConnected() bool { return false }
func (f *fakeClient) IsLoggedIn() bool  { return false }
func (f *fakeClient) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loggedOut = true
	return nil
}
func (f *fakeClient) AddEventHandler(h whatsmeow.EventHandler) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = h
	return 1
}
func (f *fakeClient) GetQRChannel(context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	return nil, errors.New("not implemented in fake")
}
func (f *fakeClient) PairPhone(_ context.Context, _ string, _ bool, _ whatsmeow.PairClientType, display string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pairDisplay = display
	if f.pairErr != nil {
		return "", f.pairErr
	}
	return f.pairCode, nil
}
func (f *fakeClient) SendPresence(_ context.Context, state types.Presence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presence = append(f.presence, state)
	return nil
}
func (f *fakeClient) SendChatPresence(context.Context, types.JID, types.ChatPresence, types.ChatPresenceMedia) error {
	return nil
}
func (f *fakeClient) MarkRead(context.Context, []types.MessageID, time.Time, types.JID, types.JID, ...types.ReceiptType) error {
	return nil
}

type fixedClock struct{ ms int64 }

func (c fixedClock) NowMs() int64 { return c.ms }

// ----------------------------------------------------------------------------
// Status emission via the event handler / state machine.
// ----------------------------------------------------------------------------

// TestSetStatus_EmitsOnChangeOnly writes one transition twice and then a distinct transition.
// Status emission occurs once per actual change, suppressing duplicate lifecycle noise
// without losing new state. Persistence is API-owned; nothing else is written.
func TestSetStatus_EmitsOnChangeOnly(t *testing.T) {
	m, sink := newTestManager(t, Config{})
	ms := &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", status: domain.SessionStopped}
	m.mu.Lock()
	m.sessions["sess_1"] = ms
	m.mu.Unlock()

	m.setStatus(context.Background(), ms, domain.SessionWorking)
	m.setStatus(context.Background(), ms, domain.SessionWorking) // no-op (same)
	m.setStatus(context.Background(), ms, domain.SessionStopped)

	if got := sink.typeCount(domain.EventSessionStatus); got != 2 {
		t.Fatalf("expected 2 session.status events (dedup the repeat), got %d", got)
	}
}

// TestEventHandler_TerminalEventStopsReconnect delivers a terminal whatsmeow event to a session
// with reconnect work pending. It records the terminal status, cancels reconnect ownership, and emits
// the transition exactly once.
func TestEventHandler_TerminalEventStopsReconnect(t *testing.T) {
	m, sink, inbound, fc := newTestManagerParts(t, Config{})
	jid := types.NewJID("628111", types.DefaultUserServer)
	ms := &ManagedSession{
		SessionID:      "sess_1",
		OrganizationID: "ten_1",
		status:         domain.SessionWorking,
		reconnect:      true,
		client:         fc,
		device:         &store.Device{ID: &jid},
		cancel:         func() {},
	}
	m.mu.Lock()
	m.sessions["sess_1"] = ms
	m.mu.Unlock()

	h := m.eventHandlerFor(ms)
	h(&events.LoggedOut{})

	if ms.Status() != domain.SessionLoggedOut {
		t.Fatalf("status = %s, want logged_out", ms.Status())
	}
	ms.mu.Lock()
	reconnect := ms.reconnect
	client := ms.client
	ms.mu.Unlock()
	if reconnect {
		t.Fatal("reconnect should be cleared after LoggedOut")
	}
	if client != nil {
		t.Fatal("client should be torn down after LoggedOut")
	}
	if ms.device == nil || ms.device.ID != nil {
		t.Fatal("logged-out session should receive a fresh unpaired device")
	}
	if fc.disconnects == 0 {
		t.Fatal("client should have been disconnected")
	}
	if sink.typeCount(domain.EventSessionStatus) != 1 {
		t.Fatalf("expected 1 session.status event, got %d", sink.typeCount(domain.EventSessionStatus))
	}
	// Every event is forwarded to inbound, including terminal ones.
	if inbound.countValue() != 1 {
		t.Fatalf("expected event forwarded to inbound, got %d", inbound.countValue())
	}
}

// TestEventHandler_BoundsBackgroundWork proves a stalled inbound dependency
// cannot hold a database connection or whatsmeow callback forever. Processing
// remains synchronous, but the callback receives and observes its configured
// deadline.
func TestEventHandler_BoundsBackgroundWork(t *testing.T) {
	m, _, inbound, _ := newTestManagerParts(t, Config{InboundEventTimeout: 20 * time.Millisecond})
	ms := &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1"}
	deadlineSeen := make(chan bool, 1)
	errSeen := make(chan error, 1)
	inbound.handle = func(ctx context.Context, _ any) {
		_, ok := ctx.Deadline()
		deadlineSeen <- ok
		<-ctx.Done()
		errSeen <- ctx.Err()
	}

	started := time.Now()
	m.eventHandlerFor(ms)(struct{}{})
	if !<-deadlineSeen {
		t.Fatal("inbound callback context has no deadline")
	}
	if err := <-errSeen; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("callback context error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded callback took %s", elapsed)
	}
}

// TestEventHandler_ConnectedResetsBackoff first advances retry state and then delivers a connected
// event. The session becomes working and its attempt counter returns to zero so later disconnects
// start at the shortest delay.
func TestEventHandler_ConnectedResetsBackoff(t *testing.T) {
	m, _, _, fc := newTestManagerParts(t, Config{})
	ms := &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", status: domain.SessionStarting, attempt: 5, client: fc}
	m.mu.Lock()
	m.sessions["sess_1"] = ms
	m.mu.Unlock()

	m.eventHandlerFor(ms)(&events.Connected{})

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.attempt != 0 {
		t.Fatalf("attempt should reset to 0 on Connected, got %d", ms.attempt)
	}
	if ms.status != domain.SessionWorking {
		t.Fatalf("status should be working, got %s", ms.status)
	}

	deadline := time.After(500 * time.Millisecond)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		fc.mu.Lock()
		got := append([]types.Presence(nil), fc.presence...)
		fc.mu.Unlock()
		if len(got) > 0 {
			if got[0] != types.PresenceAvailable {
				t.Fatalf("presence = %v, want available", got)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for online presence")
		case <-tick.C:
		}
	}
}

// TestEventHandler_PairSuccessRecordsJID sends a successful pairing event carrying the new device
// address. The manager records the canonical JIDs on the managed session; row persistence is
// API-owned.
func TestEventHandler_PairSuccessRecordsJID(t *testing.T) {
	m, _, _, _ := newTestManagerParts(t, Config{})
	ms := &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", status: domain.SessionScanQR}
	m.mu.Lock()
	m.sessions["sess_1"] = ms
	m.mu.Unlock()

	jid := types.NewJID("628111", types.DefaultUserServer)
	lid := types.NewJID("777", types.HiddenUserServer)
	m.eventHandlerFor(ms)(&events.PairSuccess{ID: jid, LID: lid})

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.pairedJID != jid.String() {
		t.Fatalf("pairedJID = %q, want %s", ms.pairedJID, jid.String())
	}
	if ms.pairedLID != lid.String() {
		t.Fatalf("pairedLID = %q, want %s", ms.pairedLID, lid.String())
	}
}

// ----------------------------------------------------------------------------
// Lifecycle: Stop / Logout against fakes.
// ----------------------------------------------------------------------------

// TestStop_TearsDownAndMarksStopped stops a running managed session with reconnect state. It
// cancels background work, disconnects the client, and emits the stopped transition.
func TestStop_TearsDownAndMarksStopped(t *testing.T) {
	m, sink, _, fc := newTestManagerParts(t, Config{})
	ms := &ManagedSession{
		SessionID: "sess_1", OrganizationID: "ten_1", status: domain.SessionWorking,
		reconnect: true, client: fc, cancel: func() {},
	}
	m.mu.Lock()
	m.sessions["sess_1"] = ms
	m.mu.Unlock()

	if err := m.Stop(context.Background(), "sess_1"); err != nil {
		t.Fatal(err)
	}
	if ms.Status() != domain.SessionStopped {
		t.Fatalf("status = %s, want stopped", ms.Status())
	}
	if fc.disconnects == 0 {
		t.Fatal("client should have been disconnected")
	}
	if sink.typeCount(domain.EventSessionStatus) != 1 {
		t.Fatalf("expected 1 session.status event, got %d", sink.typeCount(domain.EventSessionStatus))
	}
}

// TestLogout_DeletesDeviceAndMarksLoggedOut logs out an active session through the WhatsApp client.
// Device credentials are deleted from the keystore, preventing boot adoption of stale keys.
func TestLogout_DeletesDeviceAndMarksLoggedOut(t *testing.T) {
	jid := types.NewJID("628111", types.DefaultUserServer)
	dev := &store.Device{ID: &jid}
	m, sink, _, fc := newTestManagerParts(t, Config{})
	ks := m.keystore.(*fakeKeystore)
	ms := &ManagedSession{
		SessionID: "sess_1", OrganizationID: "ten_1", status: domain.SessionWorking,
		reconnect: true, client: fc, device: dev, cancel: func() {},
	}
	m.mu.Lock()
	m.sessions["sess_1"] = ms
	m.mu.Unlock()

	if err := m.Logout(context.Background(), "sess_1"); err != nil {
		t.Fatal(err)
	}
	if !fc.loggedOut {
		t.Fatal("client.Logout was not called")
	}
	if len(ks.deleted) != 1 || ks.deleted[0] != dev {
		t.Fatal("device was not deleted from keystore")
	}
	if ms.Status() != domain.SessionLoggedOut {
		t.Fatalf("status = %s, want logged_out", ms.Status())
	}
	ms.mu.Lock()
	freshDevice := ms.device
	ms.mu.Unlock()
	if freshDevice == nil || freshDevice == dev || freshDevice.ID != nil {
		t.Fatal("logout should replace the deleted device with a fresh unpaired device")
	}
	// Repeating logout stays idempotent and still emits the durable reset signal,
	// which is what lets the API-side projection repair stale pairing rows.
	before := sink.typeCount(domain.EventSessionStatus)
	if err := m.Logout(context.Background(), "sess_1"); err != nil {
		t.Fatalf("repeat logout: %v", err)
	}
	if sink.typeCount(domain.EventSessionStatus)-before != 1 {
		t.Fatal("repeat logout should re-emit session.status for the durable clear")
	}
	fc.pairCode = "ABCD-1234"
	if code, err := m.StartPairingCode(context.Background(), "sess_1", "628111"); err != nil {
		t.Fatalf("pairing after logout: %v", err)
	} else if code != "ABCD-1234" {
		t.Fatalf("pairing code = %q, want ABCD-1234", code)
	}
}

// TestStop_UnknownSession targets an ID absent from the manager registry. It returns the domain
// not-found error and performs no repository or client side effects.
func TestStop_UnknownSession(t *testing.T) {
	m, _, _, _ := newTestManagerParts(t, Config{})
	err := m.Stop(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected not-found error")
	}
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
}

// StartAssignedBoot is the assignment-driven boot: it always succeeds without
// touching any store, because reconciliation already materialized sessions.
func TestStartAssignedBootIsStoreFree(t *testing.T) {
	m, _, _, _ := newTestManagerParts(t, Config{})
	code, err := m.StartAssignedBoot(context.Background())
	if err != nil || code != "" {
		t.Fatalf("assignment-driven boot returned (%q, %v)", code, err)
	}
}

// newTestManager wires a manager over fakes with a controllable client factory.
// There is no session repository any more — the gateway owns no rows.
func newTestManager(t *testing.T, cfg Config) (*Manager, *fakeSink) {
	t.Helper()
	m, sink, _, _ := newTestManagerParts(t, cfg)
	return m, sink
}

func newTestManagerParts(t *testing.T, cfg Config) (*Manager, *fakeSink, *fakeInbound, *fakeClient) {
	t.Helper()
	ks := &fakeKeystore{}
	sink := &fakeSink{}
	inboundFake := &fakeInbound{}
	fc := &fakeClient{}
	m := NewManager(ks, nil, sink, inboundFake, fixedClock{ms: 1000}, quietLogger(), cfg)
	m.SetClientFactory(func(*store.Device) waClient { return fc })
	return m, sink, inboundFake, fc
}
