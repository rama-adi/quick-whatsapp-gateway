package wa

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

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
func (f *fakeRepo) ListByOrg(context.Context, string) ([]*domain.WASession, error) {
	return nil, nil
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
	mu   sync.Mutex
	seen []any
}

func (f *fakeInbound) Handle(_ context.Context, _, _ string, _ bool, evt any) {
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
func (c *fakeClient) PairPhone(context.Context, string, bool, whatsmeow.PairClientType, string) (string, error) {
	return "ABCD-1234", nil
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

func TestBootstrapAdmin_NeedsPairing_ReturnsCode(t *testing.T) {
	m, repo, sink, _, _ := newTestManager(t, Config{AdminNumber: "628111", AdminOrganizationID: "ten_admin"})
	code, err := m.bootstrapAdmin(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if code != "ABCD-1234" {
		t.Fatalf("expected pairing code, got %q", code)
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
}

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
