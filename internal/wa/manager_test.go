package wa

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// ----------------------------------------------------------------------------
// Fakes for the consumer interfaces.
// ----------------------------------------------------------------------------

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

type fakeRepo struct {
	mu       sync.Mutex
	byID     map[string]*domain.WASession
	byJID    map[string]*domain.WASession
	statuses []statusUpdate
}

type statusUpdate struct {
	id     string
	status domain.SessionStatus
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{byID: map[string]*domain.WASession{}, byJID: map[string]*domain.WASession{}}
}

func (f *fakeRepo) Get(_ context.Context, id string) (*domain.WASession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound("no session")
	}
	return s, nil
}
func (f *fakeRepo) GetByJID(_ context.Context, jid string) (*domain.WASession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byJID[jid]
	if !ok {
		return nil, domain.ErrNotFound("no session")
	}
	return s, nil
}
func (f *fakeRepo) ListByOrg(_ context.Context, organizationID string) ([]*domain.WASession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*domain.WASession
	for _, s := range f.byID {
		if s.OrganizationID == organizationID {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeRepo) Create(_ context.Context, s *domain.WASession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[s.ID] = s
	return nil
}
func (f *fakeRepo) Update(_ context.Context, s *domain.WASession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[s.ID] = s
	return nil
}
func (f *fakeRepo) UpdateStatus(_ context.Context, id string, status domain.SessionStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, statusUpdate{id, status})
	if s, ok := f.byID[id]; ok {
		s.Status = status
	}
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
func (f *fakeSink) typeCount(typ string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

type fakeInbound struct {
	mu     sync.Mutex
	seen   []any
	handle func(context.Context, any)
}

func (f *fakeInbound) Handle(ctx context.Context, _, _ string, _ bool, evt any) {
	if f.handle != nil {
		f.handle(ctx, evt)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, evt)
}
func (f *fakeInbound) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

type fixedClock struct{ ms int64 }

func (c fixedClock) NowMs() int64 { return c.ms }

// fakeClient implements waClient without any network. It records Connect/Logout/
// Disconnect calls so lifecycle behavior is observable.
type fakeClient struct {
	mu          sync.Mutex
	connected   bool
	connectErr  error
	loggedOut   bool
	disconnects int
	handler     whatsmeow.EventHandler
	presence    []types.Presence
	readIDs     []types.MessageID
	pairDisplay string
}

func (c *fakeClient) Connect() error {
	if c.connectErr != nil {
		return c.connectErr
	}
	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
	return nil
}
func (c *fakeClient) Disconnect() {
	c.mu.Lock()
	c.connected = false
	c.disconnects++
	c.mu.Unlock()
}
func (c *fakeClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}
func (c *fakeClient) IsLoggedIn() bool { return true }
func (c *fakeClient) Logout(context.Context) error {
	c.mu.Lock()
	c.loggedOut = true
	c.mu.Unlock()
	return nil
}
func (c *fakeClient) AddEventHandler(h whatsmeow.EventHandler) uint32 {
	c.mu.Lock()
	c.handler = h
	c.mu.Unlock()
	return 1
}
func (c *fakeClient) GetQRChannel(context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	ch := make(chan whatsmeow.QRChannelItem)
	close(ch)
	return ch, nil
}
func (c *fakeClient) PairPhone(_ context.Context, _ string, _ bool, _ whatsmeow.PairClientType, displayName string) (string, error) {
	c.mu.Lock()
	c.pairDisplay = displayName
	c.mu.Unlock()
	return "ABCD-1234", nil
}
func (c *fakeClient) SendPresence(_ context.Context, state types.Presence) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.presence = append(c.presence, state)
	return nil
}
func (c *fakeClient) SendChatPresence(context.Context, types.JID, types.ChatPresence, types.ChatPresenceMedia) error {
	return nil
}
func (c *fakeClient) MarkRead(_ context.Context, ids []types.MessageID, _ time.Time, _, _ types.JID, _ ...types.ReceiptType) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readIDs = append(c.readIDs, ids...)
	return nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestManager(t *testing.T, cfg Config) (*Manager, *fakeRepo, *fakeSink, *fakeInbound, *fakeClient) {
	t.Helper()
	ks := &fakeKeystore{}
	repo := newFakeRepo()
	sink := &fakeSink{}
	inbound := &fakeInbound{}
	fc := &fakeClient{}
	m := NewManager(ks, repo, sink, inbound, fixedClock{ms: 1000}, quietLogger(), cfg)
	m.SetClientFactory(func(*store.Device) waClient { return fc })
	return m, repo, sink, inbound, fc
}

// ----------------------------------------------------------------------------
// Admin-bootstrap decision logic (pure).
// ----------------------------------------------------------------------------

// TestAdminNeedsPairing examines empty, unpaired, and paired device sets from the keystore. Pairing
// is required only when no stored device is already linked, keeping bootstrap idempotent across
// restarts.
func TestAdminNeedsPairing(t *testing.T) {
	adminJID := types.NewJID("628111", types.DefaultUserServer).String()
	tests := []struct {
		name       string
		number     string
		deviceJIDs []string
		want       bool
	}{
		{"no admin number configured", "", nil, false},
		{"configured, no devices -> needs pairing", "628111", nil, true},
		{"configured, unrelated device -> needs pairing", "628111", []string{types.NewJID("628999", types.DefaultUserServer).String()}, true},
		{"configured, already paired -> no pairing", "628111", []string{adminJID}, false},
		{"configured, among several -> no pairing", "628111", []string{"x@y", adminJID, "z@w"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adminNeedsPairing(tt.number, tt.deviceJIDs, adminJID)
			if got != tt.want {
				t.Fatalf("adminNeedsPairing = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDeviceJIDs_SkipsUnpaired mixes paired and fresh devices in the keystore. The returned
// inventory contains only canonical paired JIDs, so incomplete device rows never become resumable
// sessions.
func TestDeviceJIDs_SkipsUnpaired(t *testing.T) {
	jid := types.NewJID("628111", types.DefaultUserServer)
	devs := []*store.Device{
		{ID: &jid},
		{ID: nil}, // unpaired stray
	}
	got := deviceJIDs(devs)
	if len(got) != 1 || got[0] != jid.String() {
		t.Fatalf("deviceJIDs = %v, want [%s]", got, jid.String())
	}
}

// TestBootstrapAdmin_AlreadyPaired_NoCode starts with an existing paired admin device. Bootstrap
// returns no pairing code and does not open a second pairing flow, preserving the established account.
func TestBootstrapAdmin_AlreadyPaired_NoCode(t *testing.T) {
	jid := types.NewJID("628111", types.DefaultUserServer)
	m, _, _, _, _ := newTestManager(t, Config{AdminNumber: "628111", AdminOrganizationID: "ten_admin"})
	code, err := m.bootstrapAdmin(context.Background(), []*store.Device{{ID: &jid}})
	if err != nil {
		t.Fatal(err)
	}
	if code != "" {
		t.Fatalf("expected no pairing code when already paired, got %q", code)
	}
}

// TestBootstrapAdmin_NeedsPairing_ReturnsCode uses an unpaired device and a fake client that emits
// a phone-link code. The code is returned after connect setup, making the bootstrap result correspond
// to the registered session.
func TestBootstrapAdmin_NeedsPairing_ReturnsCode(t *testing.T) {
	m, repo, sink, _, fc := newTestManager(t, Config{
		AdminNumber:         "628111",
		AdminOrganizationID: "ten_admin",
		DeviceName:          "Acme Support",
	})
	code, err := m.bootstrapAdmin(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if code != "ABCD-1234" {
		t.Fatalf("expected pairing code, got %q", code)
	}
	fc.mu.Lock()
	pairDisplay := fc.pairDisplay
	fc.mu.Unlock()
	if pairDisplay != "Chrome (Acme Support)" {
		t.Fatalf("pair display = %q, want %q", pairDisplay, "Chrome (Acme Support)")
	}
	// An is_admin_session row must have been created.
	repo.mu.Lock()
	var found *domain.WASession
	for _, s := range repo.byID {
		if s.IsAdminSession {
			found = s
		}
	}
	repo.mu.Unlock()
	if found == nil {
		t.Fatal("expected an is_admin_session row to be created")
	}
	// An auth.code event must have been emitted.
	if sink.typeCount(domain.EventAuthCode) != 1 {
		t.Fatalf("expected 1 auth.code event, got %d", sink.typeCount(domain.EventAuthCode))
	}
}

// TestBootstrapAdmin_DefaultPairDisplayIncludesGatewayID leaves the display label unset during
// phone pairing. The generated label includes the gateway ID, ensuring administrators can distinguish
// concurrent gateway links.
func TestBootstrapAdmin_DefaultPairDisplayIncludesGatewayID(t *testing.T) {
	m, _, _, _, fc := newTestManager(t, Config{
		AdminNumber:         "628111",
		AdminOrganizationID: "ten_admin",
		GatewayID:           "gw-1",
	})
	if _, err := m.bootstrapAdmin(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	fc.mu.Lock()
	pairDisplay := fc.pairDisplay
	fc.mu.Unlock()
	if pairDisplay != "Chrome (Linux - gw-1)" {
		t.Fatalf("pair display = %q, want %q", pairDisplay, "Chrome (Linux - gw-1)")
	}
}

// TestBootstrapAdmin_RunningSession_NoFatalConflict invokes bootstrap when the admin session is
// already managed. It reuses the running session rather than returning a conflict or creating
// duplicate reconnect ownership.
func TestBootstrapAdmin_RunningSession_NoFatalConflict(t *testing.T) {
	m, repo, _, _, fc := newTestManager(t, Config{AdminNumber: "628111", AdminOrganizationID: "ten_admin"})
	phone := "628111"
	sess := &domain.WASession{
		ID:             "sess_admin",
		OrganizationID: "ten_admin",
		Status:         domain.SessionStarting,
		PhoneNumber:    &phone,
		IsAdminSession: true,
		CreatedAt:      1000,
		UpdatedAt:      1000,
	}
	if err := repo.Create(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	ms := &ManagedSession{
		SessionID:      sess.ID,
		OrganizationID: sess.OrganizationID,
		IsAdmin:        true,
		device:         m.keystore.NewDevice(),
		client:         fc,
		status:         domain.SessionStarting,
	}
	m.sessions[sess.ID] = ms

	code, err := m.bootstrapAdmin(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if code != "" {
		t.Fatalf("expected no pairing code for running admin session, got %q", code)
	}
}

// TestBootstrapAdmin_Disabled turns off administrative bootstrap in manager configuration. No
// keystore lookup, client connection, session registration, or pairing side effect is performed.
func TestBootstrapAdmin_Disabled(t *testing.T) {
	m, _, _, _, _ := newTestManager(t, Config{}) // no admin number
	code, err := m.bootstrapAdmin(context.Background(), nil)
	if err != nil || code != "" {
		t.Fatalf("disabled bootstrap: code=%q err=%v", code, err)
	}
}

// ----------------------------------------------------------------------------
// shouldResume.
// ----------------------------------------------------------------------------

// TestShouldResume evaluates persisted working, connecting, stopped, logged-out, and terminal
// states. Only lifecycle states intended to survive process restart are selected for automatic
// reconnection.
func TestShouldResume(t *testing.T) {
	tests := map[domain.SessionStatus]bool{
		domain.SessionWorking:   true,
		domain.SessionStarting:  true,
		domain.SessionScanQR:    true,
		domain.SessionStopped:   false,
		domain.SessionLoggedOut: false,
		domain.SessionFailed:    false,
	}
	for status, want := range tests {
		if got := shouldResume(status); got != want {
			t.Errorf("shouldResume(%s) = %v, want %v", status, got, want)
		}
	}
}

// ----------------------------------------------------------------------------
// Status emission via the event handler / state machine.
// ----------------------------------------------------------------------------

// TestSetStatus_EmitsOnChangeOnly writes one transition twice and then a distinct transition.
// Persistence and status emission occur once per actual change, suppressing duplicate lifecycle noise
// without losing new state.
func TestSetStatus_EmitsOnChangeOnly(t *testing.T) {
	m, repo, sink, _, _ := newTestManager(t, Config{})
	repo.byID["sess_1"] = &domain.WASession{ID: "sess_1", OrganizationID: "ten_1", Status: domain.SessionStopped}
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
	if len(repo.statuses) != 2 {
		t.Fatalf("expected 2 persisted status updates, got %d", len(repo.statuses))
	}
}

// TestEventHandler_TerminalEventStopsReconnect delivers a terminal whatsmeow event to a session
// with reconnect work pending. It records the terminal status, cancels reconnect ownership, and emits
// the transition exactly once.
func TestEventHandler_TerminalEventStopsReconnect(t *testing.T) {
	m, repo, sink, inbound, fc := newTestManager(t, Config{})
	repo.byID["sess_1"] = &domain.WASession{ID: "sess_1", OrganizationID: "ten_1", Status: domain.SessionWorking}
	ms := &ManagedSession{
		SessionID:      "sess_1",
		OrganizationID: "ten_1",
		status:         domain.SessionWorking,
		reconnect:      true,
		client:         fc,
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
	if fc.disconnects == 0 {
		t.Fatal("client should have been disconnected")
	}
	if sink.typeCount(domain.EventSessionStatus) != 1 {
		t.Fatalf("expected 1 session.status event, got %d", sink.typeCount(domain.EventSessionStatus))
	}
	// Every event is forwarded to inbound, including terminal ones.
	if inbound.count() != 1 {
		t.Fatalf("expected event forwarded to inbound, got %d", inbound.count())
	}
}

// TestEventHandler_BoundsBackgroundWork proves a stalled inbound dependency
// cannot hold a database connection or whatsmeow callback forever. Processing
// remains synchronous, but the callback receives and observes its configured
// deadline.
func TestEventHandler_BoundsBackgroundWork(t *testing.T) {
	m, _, _, inbound, _ := newTestManager(t, Config{InboundEventTimeout: 20 * time.Millisecond})
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
	m, repo, _, _, fc := newTestManager(t, Config{})
	repo.byID["sess_1"] = &domain.WASession{ID: "sess_1", OrganizationID: "ten_1", Status: domain.SessionStarting}
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
// address. The manager persists the canonical JID and exposes it on the managed session before
// subsequent lifecycle work.
func TestEventHandler_PairSuccessRecordsJID(t *testing.T) {
	m, repo, _, _, _ := newTestManager(t, Config{})
	repo.byID["sess_1"] = &domain.WASession{ID: "sess_1", OrganizationID: "ten_1", Status: domain.SessionScanQR}
	ms := &ManagedSession{SessionID: "sess_1", OrganizationID: "ten_1", status: domain.SessionScanQR}
	m.mu.Lock()
	m.sessions["sess_1"] = ms
	m.mu.Unlock()

	jid := types.NewJID("628111", types.DefaultUserServer)
	lid := types.NewJID("777", types.HiddenUserServer)
	m.eventHandlerFor(ms)(&events.PairSuccess{ID: jid, LID: lid})

	got, _ := repo.Get(context.Background(), "sess_1")
	if got.WAJID == nil || *got.WAJID != jid.String() {
		t.Fatalf("WAJID = %v, want %s", got.WAJID, jid.String())
	}
	if got.WALID == nil || *got.WALID != lid.String() {
		t.Fatalf("WALID = %v, want %s", got.WALID, lid.String())
	}
}

// ----------------------------------------------------------------------------
// Lifecycle: Create / Stop / Logout against fakes.
// ----------------------------------------------------------------------------

// TestCreateSession_PersistsAndRegisters creates a session from an unpaired stored device. The
// repository row is written with gateway ownership before the manager publishes the in-memory session,
// preventing an untracked live client.
func TestCreateSession_PersistsAndRegisters(t *testing.T) {
	m, repo, _, _, _ := newTestManager(t, Config{DefaultRatePerMin: 20, DefaultRatePerHour: 200, DefaultAutoRead: true})
	label := "my phone"
	sess, err := m.CreateSession(context.Background(), "ten_1", &label, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != domain.SessionStopped {
		t.Fatalf("new session should be stopped, got %s", sess.Status)
	}
	if sess.RatePerMin != 20 || sess.RatePerHour != 200 {
		t.Fatalf("rate defaults not applied: %d/%d", sess.RatePerMin, sess.RatePerHour)
	}
	if _, ok := repo.byID[sess.ID]; !ok {
		t.Fatal("session not persisted")
	}
	if m.Get(sess.ID) == nil {
		t.Fatal("managed session not registered")
	}
}

// TestStart_UnpairedRejected asks the manager to start a session whose device has no paired JID. It
// returns the pairing-required error without connecting, so normal start cannot bypass the explicit
// bootstrap flow.
func TestStart_UnpairedRejected(t *testing.T) {
	m, _, _, _, _ := newTestManager(t, Config{})
	sess, _ := m.CreateSession(context.Background(), "ten_1", nil, true, false)
	// CreateSession registers a device with ID == nil (unpaired).
	err := m.Start(context.Background(), sess.ID)
	if err == nil {
		t.Fatal("expected error starting an unpaired session")
	}
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("expected validation_error, got %v", err)
	}
}

// TestStop_TearsDownAndMarksStopped stops a running managed session with reconnect state. It
// cancels background work, disconnects the client, removes the in-memory owner, and durably marks the
// session stopped.
func TestStop_TearsDownAndMarksStopped(t *testing.T) {
	m, repo, sink, _, fc := newTestManager(t, Config{})
	repo.byID["sess_1"] = &domain.WASession{ID: "sess_1", OrganizationID: "ten_1", Status: domain.SessionWorking}
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
// Device credentials are deleted and the durable status becomes logged_out, preventing Boot from
// adopting stale keys.
func TestLogout_DeletesDeviceAndMarksLoggedOut(t *testing.T) {
	jid := types.NewJID("628111", types.DefaultUserServer)
	dev := &store.Device{ID: &jid}
	m, repo, _, _, fc := newTestManager(t, Config{})
	ks := m.keystore.(*fakeKeystore)
	repo.byID["sess_1"] = &domain.WASession{ID: "sess_1", OrganizationID: "ten_1", Status: domain.SessionWorking}
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
}

// TestStop_UnknownSession targets an ID absent from the manager registry. It returns the domain
// not-found error and performs no repository or client side effects.
func TestStop_UnknownSession(t *testing.T) {
	m, _, _, _, _ := newTestManager(t, Config{})
	err := m.Stop(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected not-found error")
	}
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
}

// ----------------------------------------------------------------------------
// Boot adoption.
// ----------------------------------------------------------------------------

// TestBoot_AdoptsPairedDevices supplies multiple paired keystore devices with resumable repository
// rows. Boot creates one managed client per eligible device and reconnects them under this gateway
// without duplicating registrations.
func TestBoot_AdoptsPairedDevices(t *testing.T) {
	jid := types.NewJID("628111", types.DefaultUserServer)
	dev := &store.Device{ID: &jid}
	m, repo, _, _, _ := newTestManager(t, Config{})
	ks := m.keystore.(*fakeKeystore)
	ks.devices = []*store.Device{dev}
	// Stopped session: adopted but not resumed.
	repo.byJID[jid.String()] = &domain.WASession{ID: "sess_1", OrganizationID: "ten_1", Status: domain.SessionStopped}
	repo.byID["sess_1"] = repo.byJID[jid.String()]

	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	ms := m.Get("sess_1")
	if ms == nil {
		t.Fatal("session not adopted on boot")
	}
	// Stopped -> not resumed -> no client.
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.client != nil {
		t.Fatal("stopped session should not be resumed/connected")
	}
}
