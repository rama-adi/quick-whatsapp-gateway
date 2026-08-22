package wa

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/desiredstate"
)

// Config holds the manager's tunables, populated from ENV by the composition root.
type Config struct {
	// GatewayID is this gateway's id (GATEWAY_ID). Sessions this gateway adopts on
	// boot are pinned to it (§4.5). Empty leaves the row's gateway_id untouched.
	GatewayID string
	// DeviceName is the OS/app label shown in WhatsApp's Linked devices list for
	// newly paired companion devices.
	DeviceName string
	// InboundEventTimeout bounds one synchronous whatsmeow callback, including
	// lifecycle persistence and the inbound pipeline. Zero uses 10 seconds.
	InboundEventTimeout time.Duration
	// PresenceTimeout bounds the detached online-presence announcement. Zero uses
	// 10 seconds.
	PresenceTimeout time.Duration
	// DefaultRatePerMin / DefaultRatePerHour seed new sessions' rate limits.
	DefaultRatePerMin  int
	DefaultRatePerHour int
	// DefaultAutoRead seeds new sessions' auto_read flag.
	DefaultAutoRead bool
	// Backoff overrides the reconnect schedule; the zero value uses defaultBackoff.
	Backoff backoffConfig
}

// clientFactory builds a waClient for a device. Production wires *whatsmeow.Client;
// tests inject a fake. Keeping it as a field is how the manager stays testable
// without a real WebSocket.
type clientFactory func(device *store.Device) waClient

// Manager is the process-local owner of every ManagedSession and whatsmeow client
// assigned to this gateway (§3). Durable device keys remain in the gateway-local
// SQLite keystore; app-visible state (wa_sessions rows, status history) is owned
// by the API — control-plane assignments drive what runs here, and session
// lifecycle events reach the API through the event journal. The sessions map is
// only the live ownership index, rebuilt from assignments on boot.
//
// Lifecycle mutations serialize access to that index with mu, while each
// ManagedSession separately protects status, QR data, retry counters, and its
// cancellation handle. Event callbacks may therefore arrive concurrently with
// Stop or Logout without transferring reconnect ownership to a second goroutine.
// Repository and event-sink failures are handled at their documented boundaries:
// state transitions attempt durable persistence before notification, and boot
// reconciliation logs and skips one broken device rather than aborting all peers.
type Manager struct {
	keystore Keystore
	sink     EventSink
	inbound  InboundHandler
	clock    Clock
	log      *slog.Logger
	cfg      Config
	waLogger waLog.Logger

	// newClient is the whatsmeow client constructor. Overridable in tests via
	// SetClientFactory.
	newClient clientFactory

	mu       sync.RWMutex
	sessions map[string]*ManagedSession // keyed by app session id
}

// NewManager constructs a Manager with all collaborators injected (no globals).
// repo is unused legacy plumbing and must be nil: the gateway owns no session
// rows — the API does. log/clock may be nil (sensible defaults are used).
func NewManager(
	keystore Keystore,
	repo SessionRepo,
	sink EventSink,
	inbound InboundHandler,
	clock Clock,
	log *slog.Logger,
	cfg Config,
) *Manager {
	_ = repo // deprecated parameter; session persistence is API-owned
	if clock == nil {
		clock = realClock{}
	}
	if log == nil {
		log = slog.Default()
	}
	if cfg.Backoff == (backoffConfig{}) {
		cfg.Backoff = defaultBackoff
	}
	if cfg.InboundEventTimeout <= 0 {
		cfg.InboundEventTimeout = 10 * time.Second
	}
	if cfg.PresenceTimeout <= 0 {
		cfg.PresenceTimeout = 10 * time.Second
	}
	cfg.DeviceName = normalizedDeviceName(cfg.DeviceName, cfg.GatewayID)
	store.SetOSInfo(cfg.DeviceName, [3]uint32{1, 0, 0})
	m := &Manager{
		keystore: keystore,
		sink:     sink,
		inbound:  inbound,
		clock:    clock,
		log:      log,
		cfg:      cfg,
		waLogger: waLog.Noop,
		sessions: make(map[string]*ManagedSession),
	}
	// Default factory builds a real whatsmeow client. Overridable in tests via
	// SetClientFactory.
	m.newClient = func(device *store.Device) waClient {
		return whatsmeow.NewClient(device, m.waLogger)
	}
	return m
}

func normalizedDeviceName(name, gatewayID string) string {
	if name == "" {
		if gatewayID == "" {
			return "Linux - gateway"
		}
		return "Linux - " + gatewayID
	}
	return name
}

// SetClientFactory swaps the whatsmeow client constructor. It is a test/setup
// hook and must be called before Boot or any session lifecycle method; it is not
// safe to mutate while sessions are starting.
func (m *Manager) SetClientFactory(f clientFactory) { m.newClient = f }

// SetInboundHandler replaces the inbound event handler. It is used by the
// composition root to break the manager <-> inbound pipeline wiring cycle.
func (m *Manager) SetInboundHandler(h InboundHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inbound = h
}

// SetWALogger sets the whatsmeow logger used for newly built clients. Existing
// clients retain their logger, so callers must configure this before Boot.
func (m *Manager) SetWALogger(l waLog.Logger) { m.waLogger = l }

// Inventory returns paired local device identities for desired-state
// reconciliation. It does not inspect app-data tables: the local keystore is
// the only inventory source in control mode.
func (m *Manager) Inventory(ctx context.Context) (desiredstate.Inventory, error) {
	devices, err := m.keystore.GetAllDevices(ctx)
	if err != nil {
		return desiredstate.Inventory{}, err
	}
	result := desiredstate.Inventory{PairedJIDs: make([]string, 0, len(devices))}
	for _, device := range devices {
		if device == nil || device.ID == nil {
			continue
		}
		result.PairedJIDs = append(result.PairedJIDs, device.ID.String())
	}
	return result, nil
}

// StartAssigned materializes an assignment directly from control-plane metadata
// and its mapped local device. In particular, it never reads wa_sessions or an
// organization during boot/reconciliation.
func (m *Manager) StartAssigned(ctx context.Context, assignment desiredstate.Assignment) error {
	devices, err := m.keystore.GetAllDevices(ctx)
	if err != nil {
		return fmt.Errorf("load local devices: %w", err)
	}
	var device *store.Device
	for _, candidate := range devices {
		if candidate != nil && candidate.ID != nil && candidate.ID.String() == assignment.DeviceJID {
			device = candidate
			break
		}
	}
	if device == nil {
		return domain.ErrNotFound("assigned local device not found")
	}
	m.mu.Lock()
	ms := m.sessions[assignment.SessionID]
	if ms == nil {
		ms = &ManagedSession{SessionID: assignment.SessionID, OrganizationID: assignment.OrganizationID, device: device, status: domain.SessionStopped}
		m.sessions[assignment.SessionID] = ms
	}
	config := assignment.Config
	ms.mu.Lock()
	ms.assignedConfig = &config
	ms.mu.Unlock()
	m.mu.Unlock()
	m.startManaged(ctx, assignment.SessionID)
	return nil
}

// AssignedConfig exposes the last control-plane session configuration. It is
// intentionally in-memory; control mode does not read wa_sessions for runtime
// settings.
func (m *Manager) AssignedConfig(id string) (desiredstate.Config, bool) {
	ms := m.Get(id)
	if ms == nil {
		return desiredstate.Config{}, false
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.assignedConfig == nil {
		return desiredstate.Config{}, false
	}
	return *ms.assignedConfig, true
}

// StopAssigned stops only a locally materialized assignment. A missing runtime
// is already reconciled and is intentionally not treated as an error.
func (m *Manager) StopAssigned(ctx context.Context, sessionID string) error {
	ms := m.Get(sessionID)
	if ms == nil {
		return nil
	}
	m.teardown(ms)
	m.setStatus(ctx, ms, domain.SessionStopped)
	return nil
}

// ----------------------------------------------------------------------------
// Boot
// ----------------------------------------------------------------------------

// StartAssignedBoot materializes a runtime for every currently-owned
// assignment that desires RUN — the control-mode replacement for the legacy
// MySQL-reading Boot. It never touches wa_sessions or organizations: ownership,
// config, and device mapping all come from desired-state reconciliation, which
// has already called StartAssigned per assignment. The return value exists for
// interface stability with the legacy pairing-code bootstrap and is always "".
func (m *Manager) StartAssignedBoot(ctx context.Context) (string, error) {
	return "", nil
}

// shouldResume reports whether a session in the given persisted status should be
// reconnected on boot. STOPPED / LOGGED_OUT / FAILED stay down until the admin
// acts; everything that was live (or mid-startup) resumes.
func shouldResume(status domain.SessionStatus) {
	_ = status
}

// ----------------------------------------------------------------------------
// Public lifecycle: Start / Stop / Restart / Logout
// ----------------------------------------------------------------------------

// Get returns the ManagedSession for id, or nil when unknown.
func (m *Manager) Get(id string) *ManagedSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// EnsureDevice registers a ManagedSession with a fresh keystore device for an
// API-created session row, without inserting anything into wa_sessions — the
// API owns rows now. It mirrors CreateSession's map-entry construction and is
// the gateway-side body of PrepareSession. Idempotent: an already-known
// session is left untouched (its existing device, if any, stays).
func (m *Manager) EnsureDevice(id, organizationID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sessions[id]; exists {
		return
	}
	m.sessions[id] = &ManagedSession{
		SessionID:      id,
		OrganizationID: organizationID,
		device:         m.keystore.NewDevice(),
		status:         domain.SessionStopped,
	}
}

// ConnectionState returns a point-in-time, non-identifying runtime snapshot for
// request-failure telemetry. Status and socket/login state are reported
// separately because a persisted "working" status can briefly outlive a lost
// transport connection.
func (m *Manager) ConnectionState(id string) (status domain.SessionStatus, connected, loggedIn, found bool) {
	ms := m.Get(id)
	if ms == nil {
		return "", false, false, false
	}
	ms.mu.Lock()
	status = ms.status
	client := ms.client
	ms.mu.Unlock()
	if client != nil {
		connected = client.IsConnected()
		loggedIn = client.IsLoggedIn()
	}
	return status, connected, loggedIn, true
}

// ClientFor returns the live *whatsmeow.Client for a session, or (nil, false)
// when the session is unknown or its client is not yet constructed/connected.
// It is the bridge the outbound send path uses to reach the per-session client
// (the account-global Sender resolves the right client per request via this).
func (m *Manager) ClientFor(id string) (*whatsmeow.Client, bool) {
	ms := m.Get(id)
	if ms == nil {
		return nil, false
	}
	ms.mu.Lock()
	c := ms.client
	ms.mu.Unlock()
	if c == nil {
		return nil, false
	}
	cli, ok := c.(*whatsmeow.Client)
	if !ok {
		return nil, false
	}
	return cli, true
}

// Forget tears down a session's runtime (cancelling its goroutine and
// disconnecting its client) and drops it from the in-memory registry. It does
// NOT touch the wa_sessions row or the keystore device — the caller (the
// SessionService delete path) owns those. Safe to call for an unknown id.
func (m *Manager) Forget(id string) {
	ms := m.Get(id)
	if ms == nil {
		return
	}
	m.teardown(ms)
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// LatestQR returns the most recently streamed QR code for a session and its
// expiry (epoch-ms); code is "" when the session is unknown or no QR is
// currently available. It lets callers avoid a Get+nil dance on the runtime.
func (m *Manager) LatestQR(id string) (code string, expiresAt int64) {
	ms := m.Get(id)
	if ms == nil {
		return "", 0
	}
	return ms.LatestQR()
}

// Start connects an already-paired session and begins the reconnect loop. For
// unpaired devices use StartQR / StartPairingCode instead.
func (m *Manager) Start(ctx context.Context, id string) error {
	ms := m.Get(id)
	if ms == nil {
		return domain.ErrNotFound("session not found")
	}
	if ms.device.ID == nil {
		return domain.ErrValidation("session not paired; use QR or pairing code")
	}
	m.startManaged(ctx, id)
	return nil
}

// startManaged spins up the client + event handler + reconnect loop for a
// registered session. Idempotent: a session already running is left alone.
func (m *Manager) startManaged(parent context.Context, id string) {
	ms := m.Get(id)
	if ms == nil {
		return
	}
	ms.mu.Lock()
	if ms.client != nil {
		ms.mu.Unlock()
		return // already running
	}
	// Detach from the request context: the session lives until explicitly stopped.
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	ms.cancel = cancel
	ms.reconnect = true
	ms.attempt = 0
	client := m.newClient(ms.device)
	ms.client = client
	ms.handlerID = client.AddEventHandler(m.eventHandlerFor(ms))
	ms.mu.Unlock()

	m.setStatus(ctx, ms, domain.SessionStarting)
	go m.reconnectLoop(ctx, ms)
}

// reconnectLoop owns a single session's connection lifetime. It connects, and on
// disconnect waits backoff+jitter and retries — until the context is cancelled
// (Stop) or a terminal event clears ms.reconnect (LoggedOut/ban/…).
func (m *Manager) reconnectLoop(ctx context.Context, ms *ManagedSession) {
	// Per-session RNG seeded from crypto/rand so concurrent sessions don't share a
	// jitter schedule (avoids thundering-herd reconnects).
	rng := newSeededRand()
	for {
		ms.mu.Lock()
		if !ms.reconnect {
			ms.mu.Unlock()
			return
		}
		attempt := ms.attempt
		client := ms.client
		ms.mu.Unlock()

		if ctx.Err() != nil {
			return
		}

		if attempt > 0 {
			delay := backoffFor(m.cfg.Backoff, attempt-1, rng)
			m.log.Debug("reconnect wait", "session", ms.SessionID, "attempt", attempt, "delay", delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}

		if err := client.Connect(); err != nil {
			m.log.Warn("connect failed", "session", ms.SessionID, "attempt", attempt, "err", err)
			ms.mu.Lock()
			keep := ms.reconnect
			ms.attempt++
			ms.mu.Unlock()
			if !keep {
				return
			}
			continue
		}

		// Connect returned; whatsmeow now drives the socket and emits events. We
		// block until the context is cancelled, a terminal event clears reconnect,
		// or the connection drops (then loop to retry with backoff). Polling a
		// ticker keeps the loop simple and free of extra channels — connection
		// state is the source of truth.
		if !m.waitForReconnectSignal(ctx, ms) {
			return
		}
	}
}

// waitForReconnectSignal blocks until the session should attempt another connect
// (a disconnect occurred and reconnect is still desired) or the loop must exit
// (context cancelled, or reconnect cleared by a terminal event). It returns true
// to retry, false to exit.
func (m *Manager) waitForReconnectSignal(ctx context.Context, ms *ManagedSession) bool {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			ms.mu.Lock()
			keep := ms.reconnect
			client := ms.client
			ms.mu.Unlock()
			if !keep {
				return false
			}
			// A live, connected client needs no action. A dropped connection means
			// retry (bump attempt so the next loop applies backoff).
			if client != nil && !client.IsConnected() {
				ms.mu.Lock()
				ms.attempt++
				ms.mu.Unlock()
				return true
			}
		}
	}
}

// Stop disconnects a session and halts its reconnect loop, marking it STOPPED.
// It does NOT log out (the device keys remain in the keystore).
func (m *Manager) Stop(ctx context.Context, id string) error {
	ms := m.Get(id)
	if ms == nil {
		return domain.ErrNotFound("session not found")
	}
	m.teardown(ms)
	m.setStatus(ctx, ms, domain.SessionStopped)
	return nil
}

// Restart stops then starts a session.
func (m *Manager) Restart(ctx context.Context, id string) error {
	if err := m.Stop(ctx, id); err != nil {
		return err
	}
	return m.Start(ctx, id)
}

// Logout logs the device out of WhatsApp (server-side unlink), deletes its
// keystore device, halts reconnect, and marks the session LOGGED_OUT.
func (m *Manager) Logout(ctx context.Context, id string) error {
	ms := m.Get(id)
	if ms == nil {
		return domain.ErrNotFound("session not found")
	}
	ms.mu.Lock()
	client := ms.client
	device := ms.device
	ms.mu.Unlock()

	if client != nil {
		if err := client.Logout(ctx); err != nil {
			// Log but continue teardown — the local device should still be cleared.
			m.log.Warn("logout call failed; clearing local device anyway", "session", id, "err", err)
		}
	}
	if device != nil {
		if err := m.keystore.DeleteDevice(ctx, device); err != nil {
			m.log.Warn("delete device failed", "session", id, "err", err)
		}
	}
	m.teardown(ms)
	m.setStatus(ctx, ms, domain.SessionLoggedOut)
	return nil
}

// teardown cancels the session goroutine, disconnects the client and clears the
// per-session runtime state. It deliberately does NOT touch ms.status — the
// status transition (and its emission) is owned solely by setStatus, which the
// caller invokes afterwards. Keeping the two responsibilities separate ensures
// teardown doesn't pre-set the status and suppress setStatus's change detection.
func (m *Manager) teardown(ms *ManagedSession) {
	ms.mu.Lock()
	ms.reconnect = false
	if ms.cancel != nil {
		ms.cancel()
		ms.cancel = nil
	}
	client := ms.client
	ms.client = nil
	ms.attempt = 0
	ms.mu.Unlock()

	if client != nil {
		client.Disconnect()
	}
}

// ----------------------------------------------------------------------------
// Pairing (§6, recon §3)
// ----------------------------------------------------------------------------

// StartQR begins QR pairing for a session: it builds a fresh client, opens the
// QR channel BEFORE Connect (recon §3), connects, and pumps each refreshed code
// out as an auth.qr event. The status moves to SCAN_QR_CODE while codes stream.
func (m *Manager) StartQR(ctx context.Context, id string) error {
	ms := m.Get(id)
	if ms == nil {
		return domain.ErrNotFound("session not found")
	}
	ms.mu.Lock()
	if ms.client != nil {
		ms.mu.Unlock()
		return domain.ErrConflict("session already running")
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	ms.cancel = cancel
	ms.reconnect = true
	ms.attempt = 0
	client := m.newClient(ms.device)
	ms.client = client
	ms.handlerID = client.AddEventHandler(m.eventHandlerFor(ms))
	ms.mu.Unlock()

	qrChan, err := client.GetQRChannel(loopCtx)
	if err != nil {
		m.teardown(ms)
		m.setStatus(loopCtx, ms, domain.SessionFailed)
		return fmt.Errorf("get qr channel: %w", err)
	}
	m.setStatus(loopCtx, ms, domain.SessionScanQR)

	if err := client.Connect(); err != nil {
		m.teardown(ms)
		m.setStatus(loopCtx, ms, domain.SessionFailed)
		return fmt.Errorf("connect for qr: %w", err)
	}
	go m.pumpQR(loopCtx, ms, qrChan)
	// Once paired, PairSuccess+Connected flow through the event handler and the
	// reconnect loop keeps the session alive.
	go m.reconnectLoopAfterPair(loopCtx, ms)
	return nil
}

// pumpQR streams QR codes from whatsmeow as auth.qr events until the channel
// closes (success/timeout) or the context is cancelled.
func (m *Manager) pumpQR(ctx context.Context, ms *ManagedSession, qrChan <-chan whatsmeow.QRChannelItem) {
	for {
		select {
		case <-ctx.Done():
			return
		case item, ok := <-qrChan:
			if !ok {
				return
			}
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				ms.mu.Lock()
				ms.lastQR = item.Code
				ms.lastQRExpires = m.clock.NowMs() + item.Timeout.Milliseconds()
				ms.mu.Unlock()
				m.sink.Publish(ctx, domain.NewEvent(domain.EventAuthQR, ms.SessionID, ms.OrganizationID, map[string]any{
					"code":      item.Code,
					"timeoutMs": item.Timeout.Milliseconds(),
				}))
			case "success":
				m.log.Info("qr pairing success", "session", ms.SessionID)
				return
			case "timeout":
				m.log.Warn("qr pairing timed out", "session", ms.SessionID)
				m.setStatus(ctx, ms, domain.SessionFailed)
				return
			default:
				if item.Error != nil {
					m.log.Warn("qr pairing error", "session", ms.SessionID, "event", item.Event, "err", item.Error)
				}
			}
		}
	}
}

// StartPairingCode begins phone-number pairing and returns the linking code
// (recon §3). It builds the client, connects, requests the code, emits auth.code,
// and keeps the reconnect loop running so the eventual PairSuccess sticks.
func (m *Manager) StartPairingCode(ctx context.Context, id, phone string) (string, error) {
	ms := m.Get(id)
	if ms == nil {
		return "", domain.ErrNotFound("session not found")
	}

	ms.mu.Lock()
	if ms.client != nil {
		ms.mu.Unlock()
		return "", domain.ErrConflict("session already running")
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	ms.cancel = cancel
	ms.reconnect = true
	ms.attempt = 0
	client := m.newClient(ms.device)
	ms.client = client
	ms.handlerID = client.AddEventHandler(m.eventHandlerFor(ms))
	ms.mu.Unlock()

	m.setStatus(loopCtx, ms, domain.SessionScanQR)
	if err := client.Connect(); err != nil {
		m.teardown(ms)
		m.setStatus(loopCtx, ms, domain.SessionFailed)
		return "", fmt.Errorf("connect for pairing: %w", err)
	}
	code, err := client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, pairDisplayName(m.cfg.DeviceName))
	if err != nil {
		m.teardown(ms)
		m.setStatus(loopCtx, ms, domain.SessionFailed)
		return "", fmt.Errorf("pair phone: %w", err)
	}
	m.sink.Publish(loopCtx, domain.NewEvent(domain.EventAuthCode, ms.SessionID, ms.OrganizationID, map[string]any{
		"code":  code,
		"phone": phone,
	}))
	go m.reconnectLoopAfterPair(loopCtx, ms)
	return code, nil
}

// pairDisplayName is the "Browser (OS)" string whatsmeow validates for pairing
// codes; the server 400s on a malformed value.
func pairDisplayName(deviceName string) string {
	return fmt.Sprintf("Chrome (%s)", deviceName)
}

// reconnectLoopAfterPair keeps a freshly-connected pairing session alive: once
// PairSuccess + Connected arrive, dropped connections are retried with backoff.
// It mirrors reconnectLoop but assumes the first Connect already happened.
func (m *Manager) reconnectLoopAfterPair(ctx context.Context, ms *ManagedSession) {
	if !m.waitForReconnectSignal(ctx, ms) {
		return
	}
	m.reconnectLoop(ctx, ms)
}

// ----------------------------------------------------------------------------
// Event handling
// ----------------------------------------------------------------------------

// eventHandlerFor returns the whatsmeow EventHandler for a session: it runs the
// status state machine, then forwards EVERY event to the inbound handler.
func (m *Manager) eventHandlerFor(ms *ManagedSession) whatsmeow.EventHandler {
	return func(evt any) {
		// Event handlers fire outside any request, but their database and WhatsApp
		// work must still yield resources if a dependency stalls. Keep the callback
		// synchronous for per-client ordering while bounding its lifetime.
		ctx, cancel := context.WithTimeout(context.Background(), m.cfg.InboundEventTimeout)
		defer cancel()
		m.applyEvent(ctx, ms, evt)
		if m.inbound != nil {
			m.inbound.Handle(ctx, ms.SessionID, ms.OrganizationID, ms.IsAdmin, evt)
		}
	}
}

// applyEvent runs the status state machine for a single event and, on terminal
// events, stops the reconnect loop and tears the client down.
func (m *Manager) applyEvent(ctx context.Context, ms *ManagedSession, evt any) {
	// Capture the JID the moment pairing succeeds so the session row records it.
	if ps, ok := evt.(*events.PairSuccess); ok {
		m.recordPairedJID(ctx, ms, ps.ID, ps.LID)
	}

	t := classifyEvent(evt)

	if _, ok := evt.(*events.Connected); ok {
		// Reset backoff on a successful connection.
		ms.mu.Lock()
		ms.attempt = 0
		ms.mu.Unlock()
		// Announce "available". On a freshly-paired session the push name has not
		// synced yet, so SendPresence no-ops with ErrNoPushName — the
		// AppStateSyncComplete case below re-announces once the push name lands.
		m.sendOnlinePresence(ms)
	}

	// SendPresence requires the push name, which only arrives via app-state sync —
	// that completes AFTER Connected. (Re)announce availability when a sync lands so
	// the account actually shows online on the first connect after pairing.
	if _, ok := evt.(*events.AppStateSyncComplete); ok {
		m.sendOnlinePresence(ms)
	}

	if t.terminal {
		// LoggedOut / StreamReplaced / ban / fatal connect-failure: stop reconnect.
		// teardown clears runtime state; setStatus (below) records + emits the new
		// status.
		m.teardown(ms)
	}

	if t.changed {
		m.setStatus(ctx, ms, t.status)
	}
}

// sendOnlinePresence announces "available" for the session in the background.
// A missing push name (expected right after Connected, before app-state sync has
// delivered it) is downgraded to debug — applyEvent re-announces once the
// AppStateSyncComplete event fires, which is when presence actually sticks.
func (m *Manager) sendOnlinePresence(ms *ManagedSession) {
	ms.mu.Lock()
	client := ms.client
	ms.mu.Unlock()
	if client == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), m.cfg.PresenceTimeout)
		defer cancel()
		err := client.SendPresence(ctx, types.PresenceAvailable)
		switch {
		case err == nil:
		case errors.Is(err, whatsmeow.ErrNoPushName):
			m.log.Debug("online presence deferred until push name syncs", "session", ms.SessionID)
		default:
			m.log.Warn("send online presence failed", "session", ms.SessionID, "err", err)
		}
	}()
}

// recordPairedJID records the paired phone/LID JIDs on the managed session so
// live-ops and telemetry can read them. Persistence is API-owned: the session
// row is updated from the pairing RPC result, not by the gateway.
func (m *Manager) recordPairedJID(_ context.Context, ms *ManagedSession, jid, lid types.JID) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.pairedJID = jid.String()
	if !lid.IsEmpty() {
		ms.pairedLID = lid.String()
	}
}

// setStatus updates the in-memory status and emits a session.status event —
// but only when the status actually changed, so we don't spam duplicates.
// Persistence is API-owned: status history derives from these events after the
// API commits them.
func (m *Manager) setStatus(ctx context.Context, ms *ManagedSession, status domain.SessionStatus) {
	ms.mu.Lock()
	if ms.status == status {
		ms.mu.Unlock()
		return
	}
	ms.status = status
	ms.mu.Unlock()

	if m.sink != nil {
		m.sink.Publish(ctx, domain.NewEvent(domain.EventSessionStatus, ms.SessionID, ms.OrganizationID, map[string]any{
			"status": string(status),
		}))
	}
}

// ----------------------------------------------------------------------------
// Shutdown
// ----------------------------------------------------------------------------

// Shutdown disconnects every session and stops their loops. It does not change
// persisted status (sessions resume on next boot per shouldResume).
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.RLock()
	all := make([]*ManagedSession, 0, len(m.sessions))
	for _, ms := range m.sessions {
		all = append(all, ms)
	}
	m.mu.RUnlock()

	for _, ms := range all {
		m.teardown(ms)
	}
	return nil
}

// newSeededRand returns a *math/rand.Rand seeded from crypto/rand so each
// session's jitter schedule is independent. math/rand (not crypto) is correct
// here: we only need non-correlated, not cryptographic, randomness for jitter.
func newSeededRand() *mrand.Rand {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is unexpected; fall back to a fixed but non-zero seed.
		return mrand.New(mrand.NewSource(1))
	}
	return mrand.New(mrand.NewSource(int64(binary.LittleEndian.Uint64(b[:]))))
}
