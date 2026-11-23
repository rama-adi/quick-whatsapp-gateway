package wa

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/socket"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/desiredstate"
)

func quietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

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
	connects    int
	connected   bool
	connectErr  error
	connectGate chan struct{} // when set, Connect waits for it to close
	disconnects int
	loggedOut   bool
	logoutErr   error
	pairDisplay string
	handler     whatsmeow.EventHandler
	handlerID   uint32
	removed     []uint32
	pairCode    string
	pairErr     error

	passkeyResponses []*types.WebAuthnResponse
	passkeyConfirms  int
	passkeyErr       error
}

func (f *fakeClient) Connect() error {
	f.mu.Lock()
	gate := f.connectGate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connects++
	if f.connectErr != nil {
		return f.connectErr
	}
	f.connected = true
	return nil
}
func (f *fakeClient) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnects++
	f.connected = false
}
func (f *fakeClient) IsConnected() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.connected }
func (f *fakeClient) IsLoggedIn() bool  { return f.IsConnected() }
func (f *fakeClient) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logoutErr != nil {
		return f.logoutErr
	}
	f.loggedOut = true
	return nil
}
func (f *fakeClient) AddEventHandler(h whatsmeow.EventHandler) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlerID++
	f.handler = h
	return f.handlerID
}
func (f *fakeClient) RemoveEventHandler(id uint32) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.handler == nil || id != f.handlerID {
		return false
	}
	f.handler = nil
	f.removed = append(f.removed, id)
	return true
}

// emit delivers evt the way whatsmeow does: to the registered handler, if any.
func (f *fakeClient) emit(evt any) {
	f.mu.Lock()
	h := f.handler
	f.mu.Unlock()
	if h != nil {
		h(evt)
	}
}

func (f *fakeClient) handlerRemoved() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.removed) > 0 && f.handler == nil
}

func (f *fakeClient) SendPasskeyResponse(_ context.Context, resp *types.WebAuthnResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passkeyResponses = append(f.passkeyResponses, resp)
	return f.passkeyErr
}
func (f *fakeClient) SendPasskeyConfirmation(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passkeyConfirms++
	return f.passkeyErr
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
// Helpers
// ----------------------------------------------------------------------------

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
	m := NewManager(ks, sink, inboundFake, fixedClock{ms: 1000}, quietLogger(), cfg)
	m.SetClientFactory(func(*store.Device) waClient { return fc })
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return m, sink, inboundFake, fc
}

// attachRun installs client as the session's current run without starting a
// reconnect driver, so a test can deliver events by hand.
func attachRun(m *Manager, ms *ManagedSession, client waClient) *connection {
	ms.mu.Lock()
	ms.nextGen++
	c := newConnection(ms.nextGen, client)
	ms.conn = c
	ms.mu.Unlock()
	c.handlerID = client.AddEventHandler(m.eventHandlerFor(ms, c))
	return c
}

func register(m *Manager, ms *ManagedSession) *ManagedSession {
	m.mu.Lock()
	m.sessions[ms.SessionID] = ms
	m.mu.Unlock()
	return ms
}

func currentRun(ms *ManagedSession) *connection {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.conn
}

func statusPayloads(sink *fakeSink) []map[string]any {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out []map[string]any
	for _, e := range sink.events {
		if e.Type == domain.EventSessionStatus {
			out = append(out, e.Payload.(map[string]any))
		}
	}
	return out
}

func pairedDevice(user string) *store.Device {
	jid := types.NewJID(user, types.DefaultUserServer)
	return &store.Device{ID: &jid}
}

// ----------------------------------------------------------------------------
// Event handling
// ----------------------------------------------------------------------------

func TestEventHandler_TerminalEventEndsRun(t *testing.T) {
	m, sink, inbound, fc := newTestManagerParts(t, Config{})
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", status: domain.SessionWorking, device: pairedDevice("628111")})
	attachRun(m, ms, fc)

	fc.emit(&events.LoggedOut{})

	if ms.Status() != domain.SessionLoggedOut {
		t.Fatalf("status = %s, want logged_out", ms.Status())
	}
	if currentRun(ms) != nil {
		t.Fatal("run should end after LoggedOut")
	}
	if ms.device == nil || ms.device.ID != nil {
		t.Fatal("logged-out session should receive a fresh unpaired device")
	}
	// The handler cannot unregister itself while whatsmeow dispatches, so the
	// client closes right after the callback returns.
	waitFor(t, fc.handlerRemoved)
	waitFor(t, func() bool { fc.mu.Lock(); defer fc.mu.Unlock(); return fc.disconnects > 0 })
	if got := sink.typeCount(domain.EventSessionStatus); got != 1 {
		t.Fatalf("session.status events = %d, want 1", got)
	}
	// Every event of the current run is forwarded to inbound, terminal ones too.
	if inbound.countValue() != 1 {
		t.Fatalf("expected event forwarded to inbound, got %d", inbound.countValue())
	}
}

// TestEventHandler_BoundsBackgroundWork proves a stalled inbound dependency
// cannot hold a database connection or whatsmeow callback forever.
func TestEventHandler_BoundsBackgroundWork(t *testing.T) {
	m, _, inbound, fc := newTestManagerParts(t, Config{InboundEventTimeout: 20 * time.Millisecond})
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1"})
	c := attachRun(m, ms, fc)
	deadlineSeen := make(chan bool, 1)
	errSeen := make(chan error, 1)
	inbound.handle = func(ctx context.Context, _ any) {
		_, ok := ctx.Deadline()
		deadlineSeen <- ok
		<-ctx.Done()
		errSeen <- ctx.Err()
	}

	started := time.Now()
	m.eventHandlerFor(ms, c)(struct{}{})
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

// TestEventHandler_ConnectedResetsAttempts: a Connected event makes the run
// working, clears its failure count and announces online presence.
func TestEventHandler_ConnectedResetsAttempts(t *testing.T) {
	m, _, _, fc := newTestManagerParts(t, Config{})
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", status: domain.SessionStarting, attempt: 5})
	attachRun(m, ms, fc)

	fc.emit(&events.Connected{})

	ms.mu.Lock()
	attempt, status := ms.attempt, ms.status
	ms.mu.Unlock()
	if attempt != 0 || status != domain.SessionWorking {
		t.Fatalf("attempt = %d, status = %s; want 0, working", attempt, status)
	}
	waitFor(t, func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		return len(fc.presence) > 0 && fc.presence[0] == types.PresenceAvailable
	})
}

// TestStaleRunEventsAreDropped pins the connection generation: once a run is
// replaced, a late Connected or LoggedOut from the old client changes nothing
// about the new run.
func TestStaleRunEventsAreDropped(t *testing.T) {
	m, sink, inbound, _ := newTestManagerParts(t, Config{})
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", status: domain.SessionStopped, device: pairedDevice("628111")})
	old := &fakeClient{}
	oldRun := attachRun(m, ms, old)
	staleHandler := m.eventHandlerFor(ms, oldRun)

	m.stopRun(ms)
	m.setState(context.Background(), ms, nil, domain.SessionStopped, "")
	if !old.handlerRemoved() {
		t.Fatal("stopping a run must unregister its event handler")
	}
	fresh := &fakeClient{}
	newRun := attachRun(m, ms, fresh)
	before := sink.typeCount(domain.EventSessionStatus)

	// whatsmeow may still be dispatching an event it read before the stop.
	staleHandler(&events.Connected{})
	staleHandler(&events.LoggedOut{})
	staleHandler(&events.StreamReplaced{})

	if currentRun(ms) != newRun || ms.Status() != domain.SessionStopped {
		t.Fatalf("stale events changed the session: run=%v status=%s", currentRun(ms) == newRun, ms.Status())
	}
	if ms.device == nil || ms.device.ID == nil {
		t.Fatal("a stale LoggedOut reset the device")
	}
	if got := sink.typeCount(domain.EventSessionStatus); got != before {
		t.Fatalf("stale events published %d status events", got-before)
	}
	if inbound.countValue() != 0 {
		t.Fatal("stale lifecycle events reached the inbound pipeline")
	}
	fresh.mu.Lock()
	disconnects := fresh.disconnects
	fresh.mu.Unlock()
	if disconnects != 0 {
		t.Fatal("a stale terminal event tore down the new client")
	}
}

// ----------------------------------------------------------------------------
// Reconnect driver
// ----------------------------------------------------------------------------

// TestBuildClientDisablesWhatsmeowAutoReconnect: the manager is the only
// reconnect driver, so whatsmeow's own must be off on every client.
func TestBuildClientDisablesWhatsmeowAutoReconnect(t *testing.T) {
	m, _ := newTestManager(t, Config{})
	m.newClient = func(d *store.Device) waClient { return whatsmeow.NewClient(d, nil) }
	cli := m.buildClient(&store.Device{}).(*whatsmeow.Client)
	if cli.EnableAutoReconnect {
		t.Fatal("whatsmeow auto-reconnect would compete with the manager's driver")
	}
}

// TestAlreadyConnectedIsSuccess: ErrAlreadyConnected means the socket is up
// (whatsmeow reconnected it itself after pairing); it must not count as a
// failed attempt or loop.
func TestAlreadyConnectedIsSuccess(t *testing.T) {
	m, _, _, fc := newTestManagerParts(t, Config{MaxConnectAttempts: 1, Backoff: backoffConfig{base: time.Millisecond, max: time.Millisecond, factor: 1}})
	fc.connectErr = whatsmeow.ErrAlreadyConnected
	fc.connected = true
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", device: pairedDevice("628111")})
	ms.op.Lock()
	c := m.beginRun(ms)
	ms.op.Unlock()
	select {
	case <-c.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("run never became ready")
	}
	time.Sleep(20 * time.Millisecond)
	ms.mu.Lock()
	attempt, status := ms.attempt, ms.status
	ms.mu.Unlock()
	if attempt != 0 || status == domain.SessionFailed || currentRun(ms) != c {
		t.Fatalf("attempt = %d, status = %s; ErrAlreadyConnected was treated as a failure", attempt, status)
	}
	fc.mu.Lock()
	connects := fc.connects
	fc.mu.Unlock()
	if connects != 1 {
		t.Fatalf("connects = %d; the driver looped on ErrAlreadyConnected", connects)
	}
}

// TestConnectAttemptsExhaustedFailsSession: the driver stops retrying after
// MaxConnectAttempts and reports failed{connect_error} instead of retrying
// silently forever.
func TestConnectAttemptsExhaustedFailsSession(t *testing.T) {
	m, sink, _, fc := newTestManagerParts(t, Config{
		MaxConnectAttempts: 3,
		Backoff:            backoffConfig{base: time.Millisecond, max: time.Millisecond, factor: 1},
	})
	fc.connectErr = errors.New("dial tcp: connection refused")
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", device: pairedDevice("628111")})
	ms.op.Lock()
	m.beginRun(ms)
	ms.op.Unlock()

	waitFor(t, func() bool { return ms.Status() == domain.SessionFailed })
	ms.mu.Lock()
	reason, attempt := ms.failure, ms.attempt
	ms.mu.Unlock()
	if reason != domain.FailureConnectError || attempt != 3 || currentRun(ms) != nil {
		t.Fatalf("reason = %q, attempts = %d, running = %v", reason, attempt, currentRun(ms) != nil)
	}
	last := statusPayloads(sink)
	if got := last[len(last)-1]; got["status"] != "failed" || got["failureReason"] != "connect_error" {
		t.Fatalf("last status event = %+v", got)
	}
}

// TestRetryableConnectError: HTTP statuses WhatsApp will keep answering (not
// a timeout or server error) and a deleted device are not retried.
func TestRetryableConnectError(t *testing.T) {
	for code, want := range map[int]bool{403: false, 401: false, 408: true, 503: true} {
		if got := retryableConnectError(socket.ErrWithStatusCode{StatusCode: code}); got != want {
			t.Errorf("status %d retryable = %v, want %v", code, got, want)
		}
	}
	if retryableConnectError(store.ErrDeviceDeleted) || !retryableConnectError(errors.New("dial tcp: timeout")) {
		t.Error("device-deleted must not retry; network errors must")
	}
}

// TestRefusedConnectionFailsAtOnce: a refusal is not retried.
func TestRefusedConnectionFailsAtOnce(t *testing.T) {
	m, _, _, fc := newTestManagerParts(t, Config{MaxConnectAttempts: 5})
	fc.connectErr = store.ErrDeviceDeleted
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", device: pairedDevice("628111")})
	ms.op.Lock()
	m.beginRun(ms)
	ms.op.Unlock()
	waitFor(t, func() bool { return ms.Status() == domain.SessionFailed })
	ms.mu.Lock()
	reason, attempt := ms.failure, ms.attempt
	ms.mu.Unlock()
	if reason != domain.FailureConnectRejected || attempt != 1 {
		t.Fatalf("reason = %q after %d attempts", reason, attempt)
	}
}

// TestTransientDisconnectWithinGraceIsNotPublished: a drop that recovers
// within the grace period publishes nothing; WhatsApp drops connections
// several times a day and each one would look like an outage.
func TestTransientDisconnectWithinGraceIsNotPublished(t *testing.T) {
	m, sink, _, fc := newTestManagerParts(t, Config{
		ReconnectGrace: 200 * time.Millisecond,
		Backoff:        backoffConfig{base: time.Millisecond, max: time.Millisecond, factor: 1},
	})
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", device: pairedDevice("628111")})
	ms.op.Lock()
	c := m.beginRun(ms)
	ms.op.Unlock()
	<-c.ready
	fc.emit(&events.Connected{})
	before := len(statusPayloads(sink))

	fc.mu.Lock()
	fc.connected = false
	fc.mu.Unlock()
	fc.emit(&events.Disconnected{})
	waitFor(t, func() bool { fc.mu.Lock(); defer fc.mu.Unlock(); return fc.connects >= 2 })
	fc.emit(&events.Connected{})
	time.Sleep(300 * time.Millisecond)

	if got := statusPayloads(sink)[before:]; len(got) != 0 {
		t.Fatalf("a drop that recovered within the grace period published %+v", got)
	}
	if ms.Status() != domain.SessionWorking {
		t.Fatalf("status = %s", ms.Status())
	}
}

// TestDisconnectBeyondGraceIsPublished: a drop that outlasts the grace period
// is reported once as reconnecting (with attempt and last error), then the
// recovery is reported as working.
func TestDisconnectBeyondGraceIsPublished(t *testing.T) {
	m, sink, _, fc := newTestManagerParts(t, Config{
		ReconnectGrace: 30 * time.Millisecond,
		Backoff:        backoffConfig{base: 50 * time.Millisecond, max: 50 * time.Millisecond, factor: 1},
	})
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", device: pairedDevice("628111")})
	ms.op.Lock()
	c := m.beginRun(ms)
	ms.op.Unlock()
	<-c.ready
	fc.emit(&events.Connected{})
	before := len(statusPayloads(sink))

	fc.mu.Lock()
	fc.connected = false
	fc.connectErr = errors.New("network is unreachable")
	fc.mu.Unlock()
	fc.emit(&events.Disconnected{})
	waitFor(t, func() bool { return ms.Status() == domain.SessionReconnecting })

	fc.mu.Lock()
	fc.connectErr = nil
	fc.mu.Unlock()
	waitFor(t, func() bool { fc.mu.Lock(); defer fc.mu.Unlock(); return fc.connected })
	fc.emit(&events.Connected{})
	got := statusPayloads(sink)[before:]
	if len(got) != 2 || got[0]["status"] != "reconnecting" || got[1]["status"] != "working" {
		t.Fatalf("status events after a long drop = %+v", got)
	}
	if got[0]["attempt"] == nil || got[0]["lastError"] == nil {
		t.Fatalf("reconnecting event lacks attempt/lastError: %+v", got[0])
	}
}

// ----------------------------------------------------------------------------
// Lifecycle operations
// ----------------------------------------------------------------------------

// TestLifecycleOperationsAreSerialized: a second operation waits for the first
// one to finish instead of interleaving its read-then-act steps (here: a
// QR start must not run while a phone pairing is still connecting).
func TestLifecycleOperationsAreSerialized(t *testing.T) {
	m, _, _, fc := newTestManagerParts(t, Config{})
	fc.pairCode = "ABCD1234"
	gate := make(chan struct{})
	fc.connectGate = gate
	register(m, &ManagedSession{SessionID: "session", OrganizationID: "org", device: &store.Device{}, status: domain.SessionStopped})

	pairDone := make(chan error, 1)
	go func() {
		_, err := m.StartPairingCode(context.Background(), "session", "6281234567890")
		pairDone <- err
	}()
	waitFor(t, func() bool { return currentRun(m.Get("session")) != nil })

	qrDone := make(chan error, 1)
	go func() { qrDone <- m.StartAssigned(context.Background(), runAssignment("session", "", 0)) }()
	select {
	case <-qrDone:
		t.Fatal("a start ran while StartPairingCode held the session")
	case <-time.After(50 * time.Millisecond):
	}
	close(gate)
	if err := <-pairDone; err != nil {
		t.Fatal(err)
	}
	if err := <-qrDone; err != nil {
		t.Fatal(err)
	}
	fc.mu.Lock()
	connects := fc.connects
	fc.mu.Unlock()
	if connects != 1 {
		t.Fatalf("connects = %d; the QR start opened a second connection", connects)
	}
}

// TestLogoutReportsUnlinkFailure: when WhatsApp does not confirm the unlink,
// Logout fails and keeps the device, so the caller can retry.
func TestLogoutReportsUnlinkFailure(t *testing.T) {
	m, _, _, fc := newTestManagerParts(t, Config{})
	ks := m.keystore.(*fakeKeystore)
	fc.logoutErr = errors.New("server returned 500")
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", device: pairedDevice("628111")})
	c := attachRun(m, ms, fc)
	c.markLoggedIn()

	if err := m.Logout(context.Background(), "sess_1"); err == nil {
		t.Fatal("logout reported success although WhatsApp did not unlink the device")
	}
	if len(ks.deleted) != 0 || ms.Status() == domain.SessionLoggedOut || ms.device.ID == nil {
		t.Fatalf("failed logout cleared the device: deleted=%d status=%s", len(ks.deleted), ms.Status())
	}

	fc.logoutErr = nil
	if err := m.Logout(context.Background(), "sess_1"); err != nil {
		t.Fatal(err)
	}
	if len(ks.deleted) != 1 || ms.Status() != domain.SessionLoggedOut || ms.device.ID != nil || currentRun(ms) != nil {
		t.Fatalf("logout did not clear the session: deleted=%d status=%s", len(ks.deleted), ms.Status())
	}
}

// TestLogoutConnectsStoppedSession: unlinking a stopped session first
// connects it, since the unlink is a request to WhatsApp.
func TestLogoutConnectsStoppedSession(t *testing.T) {
	m, _, _, fc := newTestManagerParts(t, Config{})
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", device: pairedDevice("628111"), status: domain.SessionStopped})
	done := make(chan error, 1)
	go func() { done <- m.Logout(context.Background(), "sess_1") }()
	waitFor(t, func() bool { fc.mu.Lock(); defer fc.mu.Unlock(); return fc.handler != nil && fc.connected })
	fc.emit(&events.Connected{})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	fc.mu.Lock()
	loggedOut := fc.loggedOut
	fc.mu.Unlock()
	if !loggedOut || ms.Status() != domain.SessionLoggedOut {
		t.Fatalf("logged out = %v, status = %s", loggedOut, ms.Status())
	}
}

// TestLiveOpsRequireWorkingSession: live operations on a session that is not
// working answer 503 session_not_ready, not 501.
func TestLiveOpsRequireWorkingSession(t *testing.T) {
	m, _, _, fc := newTestManagerParts(t, Config{})
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", status: domain.SessionStarting})
	attachRun(m, ms, fc)
	_, err := m.readyClient("sess_1")
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeUnavailable || apiErr.Details["reason"] != "session_not_ready" {
		t.Fatalf("not-ready error = %v", err)
	}
	fc.emit(&events.Connected{})
	if _, err := m.readyClient("sess_1"); err != nil {
		t.Fatalf("working session rejected: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Pairing
// ----------------------------------------------------------------------------

func TestQRTimeoutReleasesRuntimeAndCachedCode(t *testing.T) {
	manager, _, _, client := newTestManagerParts(t, Config{})
	ms := register(manager, &ManagedSession{SessionID: "session", OrganizationID: "org", device: &store.Device{}, status: domain.SessionStopped})
	c := attachRun(manager, ms, client)
	ms.lastQR, ms.lastQRExpires = "expired-code", time.Now().Add(time.Minute).UnixMilli()
	// No codes left: the emitter reaches the end of the run at once.
	manager.emitQRCodes(c.ctx, ms, c, nil)
	if currentRun(ms) != nil || ms.lastQR != "" || ms.lastQRExpires != 0 || c.ctx.Err() == nil {
		t.Fatal("timed-out pairing retained its run or QR code")
	}
	ms.mu.Lock()
	status, reason := ms.status, ms.failure
	ms.mu.Unlock()
	if status != domain.SessionFailed || reason != domain.FailureQRTimeout {
		t.Fatalf("status = %s, reason = %q; want failed qr_timeout", status, reason)
	}
	// A new start (generation) starts a fresh run rather than reporting an
	// idempotent success for the timed-out one.
	fresh := &fakeClient{}
	manager.SetClientFactory(func(*store.Device) waClient { return fresh })
	if err := manager.StartAssigned(context.Background(), runAssignment("session", "", 1)); err != nil {
		t.Fatal(err)
	}
	if run := currentRun(ms); run == nil || run.client != fresh {
		t.Fatal("pairing retry did not start a fresh client")
	}
}

func TestManagerLatestQRDoesNotReturnExpiredCode(t *testing.T) {
	manager := NewManager(&fakeKeystore{}, nil, nil, nil, quietLogger(), Config{})
	ms := register(manager, &ManagedSession{SessionID: "session", OrganizationID: "org", device: &store.Device{}, status: domain.SessionStopped})
	ms.lastQR, ms.lastQRExpires = "old-code", time.Now().Add(-time.Second).UnixMilli()
	if code, expiry := manager.LatestQR("session"); code != "" || expiry != 0 {
		t.Fatalf("expired QR = %q, %d", code, expiry)
	}
}

// Starting a session opens a QR flow, so phone-number pairing must reuse that
// running unpaired client instead of refusing it as "already running".
func TestStartPairingCodeReusesRunningQRClient(t *testing.T) {
	manager, sink, _, client := newTestManagerParts(t, Config{})
	client.pairCode = "ABCD1234"
	ms := register(manager, &ManagedSession{SessionID: "session", OrganizationID: "org", device: &store.Device{}, status: domain.SessionStopped})
	c := attachRun(manager, ms, client)
	c.markReady()

	code, err := manager.StartPairingCode(context.Background(), "session", "6281234567890")
	if err != nil {
		t.Fatal(err)
	}
	if code != "ABCD1234" || client.pairDisplay != "Chrome (Linux)" {
		t.Fatalf("code = %q, display = %q", code, client.pairDisplay)
	}
	if currentRun(ms) != c || client.disconnects != 0 {
		t.Fatal("phone pairing replaced or disconnected the running QR client")
	}
	if sink.typeCount(domain.EventAuthCode) != 1 || ms.Status() != domain.SessionScanQR {
		t.Fatalf("auth.code events = %d, status = %q", sink.typeCount(domain.EventAuthCode), ms.Status())
	}

	// A failed code request leaves the QR flow running.
	client.pairErr = errors.New("bad-request")
	if _, err := manager.StartPairingCode(context.Background(), "session", "6281234567890"); err == nil {
		t.Fatal("pair error was not returned")
	}
	if currentRun(ms) != c || ms.Status() == domain.SessionFailed {
		t.Fatal("a failed code request tore down the QR flow")
	}
}

// ----------------------------------------------------------------------------
// Start generations and the failure latch
// ----------------------------------------------------------------------------

func runAssignment(id, device string, generation uint64) desiredstate.Assignment {
	return desiredstate.Assignment{
		SessionID: id, OrganizationID: "org", DeviceJID: device, AssignmentEpoch: 1,
		DesiredRun: true, StartGeneration: generation, Config: desiredstate.Config{Revision: 1},
	}
}

// TestFailureLatchHoldsUntilANewGeneration: a run that ended terminally is not
// restarted by heartbeat snapshots (same generation); a newer generation (the
// user asked again) starts it, and an older one is ignored.
func TestFailureLatchHoldsUntilANewGeneration(t *testing.T) {
	m, _, _, fc := newTestManagerParts(t, Config{})
	jid := types.NewJID("628111", types.DefaultUserServer)
	m.keystore.(*fakeKeystore).devices = []*store.Device{{ID: &jid}}
	ctx := context.Background()

	if err := m.StartAssigned(ctx, runAssignment("sess_1", jid.String(), 1)); err != nil {
		t.Fatal(err)
	}
	ms := m.Get("sess_1")
	run := currentRun(ms)
	<-run.ready
	fc.emit(&events.StreamReplaced{})
	waitFor(t, func() bool { return ms.Status() == domain.SessionFailed && currentRun(ms) == nil })

	for range 3 { // heartbeat snapshots repeat the generation
		if err := m.StartAssigned(ctx, runAssignment("sess_1", jid.String(), 1)); err != nil {
			t.Fatal(err)
		}
	}
	if currentRun(ms) != nil || ms.Status() != domain.SessionFailed {
		t.Fatal("a snapshot restarted a session whose run failed")
	}
	if err := m.StopAssigned(ctx, "sess_1"); err != nil {
		t.Fatal(err)
	}
	if ms.Status() != domain.SessionFailed {
		t.Fatal("stopping a failed session overwrote its failure")
	}
	if err := m.StartAssigned(ctx, runAssignment("sess_1", jid.String(), 0)); err != nil || currentRun(ms) != nil {
		t.Fatal("an older generation started the session")
	}
	if err := m.StartAssigned(ctx, runAssignment("sess_1", jid.String(), 2)); err != nil {
		t.Fatal(err)
	}
	if currentRun(ms) == nil || ms.Status() != domain.SessionStarting {
		t.Fatalf("a new generation did not start the failed session: %s", ms.Status())
	}
}

// TestNewGenerationRestartsRunningSession: restart is a new generation; a
// running session ends its run and starts a fresh one.
func TestNewGenerationRestartsRunningSession(t *testing.T) {
	m, _, _, _ := newTestManagerParts(t, Config{})
	ctx := context.Background()
	if err := m.StartAssigned(ctx, runAssignment("sess_1", "", 1)); err != nil {
		t.Fatal(err)
	}
	ms := m.Get("sess_1")
	first := currentRun(ms)
	if err := m.StartAssigned(ctx, runAssignment("sess_1", "", 1)); err != nil || currentRun(ms) != first {
		t.Fatal("a repeated snapshot restarted a running session")
	}
	if err := m.StartAssigned(ctx, runAssignment("sess_1", "", 2)); err != nil {
		t.Fatal(err)
	}
	if second := currentRun(ms); second == nil || second == first || first.ctx.Err() == nil {
		t.Fatal("a new generation did not replace the running session's run")
	}
}

// TestUnpairedRunAssignmentResumesPairing: a RUN assignment without a device
// is QR pairing, so a gateway that restarted mid-pairing starts a fresh
// pairing run instead of leaving the session starting forever.
func TestUnpairedRunAssignmentResumesPairing(t *testing.T) {
	m, sink, _, fc := newTestManagerParts(t, Config{})
	if err := m.StartAssigned(context.Background(), runAssignment("sess_1", "", 3)); err != nil {
		t.Fatal(err)
	}
	ms := m.Get("sess_1")
	run := currentRun(ms)
	if run == nil || ms.device == nil || ms.device.ID != nil {
		t.Fatal("no pairing run for an unpaired RUN assignment")
	}
	<-run.ready
	fc.emit(&events.QR{Codes: []string{"c1", "c2", "c3", "c4", "c5", "c6"}})
	waitFor(t, func() bool { return len(eventsOfType(sink, domain.EventAuthQR)) == 1 })
	if ms.Status() != domain.SessionScanQR {
		t.Fatalf("status = %s", ms.Status())
	}
}

// TestStatusSequenceIsMonotonic: every published status carries a strictly
// larger statusSeq, also across a manager restart on the same clock, and the
// report returns the current one.
func TestStatusSequenceIsMonotonic(t *testing.T) {
	m, sink, _, fc := newTestManagerParts(t, Config{})
	ms := register(m, &ManagedSession{SessionID: "sess_1", OrganizationID: "org"})
	attachRun(m, ms, fc)
	for _, status := range []domain.SessionStatus{domain.SessionStarting, domain.SessionWorking, domain.SessionStarting, domain.SessionWorking} {
		m.setState(context.Background(), ms, nil, status, "")
	}
	var last uint64
	for _, payload := range statusPayloads(sink) {
		seq := payload["statusSeq"].(uint64)
		if seq <= last {
			t.Fatalf("statusSeq %d after %d", seq, last)
		}
		last = seq
	}
	if got := m.SessionRuntime("sess_1"); got.Seq != last || got.Status != domain.SessionWorking {
		t.Fatalf("runtime = %+v, want seq %d", got, last)
	}
	// A restarted gateway (same wall clock here) starts above the old values
	// as long as its clock moved on; an untouched session reads as stopped at
	// the new process's boot sequence.
	restarted := NewManager(&fakeKeystore{}, nil, nil, fixedClock{ms: 2000}, quietLogger(), Config{})
	if got := restarted.SessionRuntime("sess_1"); got.Status != domain.SessionStopped || got.Seq <= last {
		t.Fatalf("restarted runtime = %+v, want stopped above %d", got, last)
	}
}

// TestPairingReconnectIsNotReportedAsReconnecting: a pairing connection that
// dropped and came back delivers new QR codes but no Connected event; the new
// codes count as recovery, so the grace timer does not report reconnecting.
func TestPairingReconnectIsNotReportedAsReconnecting(t *testing.T) {
	m, sink, _, fc := newTestManagerParts(t, Config{
		ReconnectGrace: 100 * time.Millisecond,
		Backoff:        backoffConfig{base: time.Millisecond, max: time.Millisecond, factor: 1},
	})
	if err := m.StartAssigned(context.Background(), runAssignment("sess_1", "", 1)); err != nil {
		t.Fatal(err)
	}
	ms := m.Get("sess_1")
	<-currentRun(ms).ready
	fc.emit(&events.QR{Codes: []string{"a1", "a2", "a3", "a4", "a5", "a6"}})
	waitFor(t, func() bool { return ms.Status() == domain.SessionScanQR })

	fc.mu.Lock()
	fc.connected = false
	fc.mu.Unlock()
	fc.emit(&events.Disconnected{})
	waitFor(t, func() bool { fc.mu.Lock(); defer fc.mu.Unlock(); return fc.connects >= 2 })
	fc.emit(&events.QR{Codes: []string{"b1", "b2", "b3", "b4", "b5", "b6"}})
	time.Sleep(200 * time.Millisecond)

	for _, payload := range statusPayloads(sink) {
		if payload["status"] == "reconnecting" {
			t.Fatalf("a recovered pairing connection was reported as reconnecting: %+v", statusPayloads(sink))
		}
	}
	if code, _ := m.LatestQR("sess_1"); code != "b1" {
		t.Fatalf("latest QR = %q, want the new set", code)
	}
}
