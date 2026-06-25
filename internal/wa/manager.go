package wa

import (
	"context"
	"crypto/rand"
	"encoding/binary"
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
)

// pairDisplayName is the "Browser (OS)" string whatsmeow validates for pairing
// codes; the server 400s on a malformed value (recon §3).
const pairDisplayName = "Chrome (Linux)"

// Config holds the manager's tunables, populated from ENV by Phase 3.
type Config struct {
	// AdminNumber is WHATSAPP_ADMIN_NUMBER (digits only, no '+'). Empty disables
	// admin-number bootstrap (§6).
	AdminNumber string
	// AdminTenantID is the tenant the bootstrapped admin session belongs to.
	AdminTenantID string
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

// Manager owns every ManagedSession and their whatsmeow clients (§3). It loads
// devices on boot, drives connect/reconnect with backoff+jitter, runs the status
// state machine, and forwards every whatsmeow event to the inbound handler.
type Manager struct {
	keystore Keystore
	repo     SessionRepo
	sink     EventSink
	inbound  InboundHandler
	clock    Clock
	log      *slog.Logger
	cfg      Config
	waLogger waLog.Logger

	newClient clientFactory

	mu       sync.RWMutex
	sessions map[string]*ManagedSession // keyed by app session id
}

// NewManager constructs a Manager with all collaborators injected (no globals).
// log/clock may be nil (sensible defaults are used).
func NewManager(
	keystore Keystore,
	repo SessionRepo,
	sink EventSink,
	inbound InboundHandler,
	clock Clock,
	log *slog.Logger,
	cfg Config,
) *Manager {
	if clock == nil {
		clock = realClock{}
	}
	if log == nil {
		log = slog.Default()
	}
	if cfg.Backoff == (backoffConfig{}) {
		cfg.Backoff = defaultBackoff
	}
	m := &Manager{
		keystore: keystore,
		repo:     repo,
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

// SetClientFactory swaps the whatsmeow client constructor. Intended for tests.
func (m *Manager) SetClientFactory(f clientFactory) { m.newClient = f }

// SetWALogger sets the whatsmeow logger used for newly built clients.
func (m *Manager) SetWALogger(l waLog.Logger) { m.waLogger = l }

// ----------------------------------------------------------------------------
// Boot
// ----------------------------------------------------------------------------

// Boot loads every persisted device from the keystore, wires it to its app
// session row, and starts those that should be running. It then performs
// admin-number bootstrap (§6). Returns the admin pairing code, if one was
// produced (empty otherwise), so the caller can surface it.
func (m *Manager) Boot(ctx context.Context) (adminPairingCode string, err error) {
	devices, err := m.keystore.GetAllDevices(ctx)
	if err != nil {
		return "", fmt.Errorf("load devices: %w", err)
	}
	for _, dev := range devices {
		if dev.ID == nil {
			// Unpaired stray device; nothing to resume.
			continue
		}
		jid := dev.ID.String()
		sess, lookupErr := m.repo.GetByJID(ctx, jid)
		if lookupErr != nil {
			m.log.Warn("boot: no session row for device, skipping", "jid", jid, "err", lookupErr)
			continue
		}
		if err := m.adopt(ctx, sess, dev); err != nil {
			m.log.Error("boot: adopt session failed", "session", sess.ID, "err", err)
			continue
		}
		// Resume sessions that were meant to be live.
		if shouldResume(sess.Status) {
			m.startManaged(ctx, sess.ID)
		}
	}

	// Admin-number bootstrap (§6).
	code, err := m.bootstrapAdmin(ctx, devices)
	if err != nil {
		return "", err
	}
	return code, nil
}

// shouldResume reports whether a session in the given persisted status should be
// reconnected on boot. STOPPED / LOGGED_OUT / FAILED stay down until the admin
// acts; everything that was live (or mid-startup) resumes.
func shouldResume(status domain.SessionStatus) bool {
	switch status {
	case domain.SessionStopped, domain.SessionLoggedOut, domain.SessionFailed:
		return false
	default:
		return true
	}
}

// adopt registers a ManagedSession for an already-paired device without starting
// it (start is a separate, explicit step).
func (m *Manager) adopt(ctx context.Context, sess *domain.WASession, dev *store.Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sessions[sess.ID]; exists {
		return nil
	}
	ms := &ManagedSession{
		SessionID: sess.ID,
		TenantID:  sess.TenantID,
		IsAdmin:   sess.IsAdminSession,
		device:    dev,
		status:    sess.Status,
	}
	m.sessions[sess.ID] = ms
	return nil
}

// ----------------------------------------------------------------------------
// Admin-number bootstrap (§6)
// ----------------------------------------------------------------------------

// adminNeedsPairing decides, purely, whether the admin number requires pairing:
// true when an admin number is configured AND no persisted device is logged in
// for it. devices are the keystore devices; adminJID is the admin number's
// phone JID string (user@s.whatsapp.net). This is the unit-tested decision core.
func adminNeedsPairing(adminNumber string, deviceJIDs []string, adminJID string) bool {
	if adminNumber == "" {
		return false
	}
	for _, j := range deviceJIDs {
		if j == adminJID {
			return false
		}
	}
	return true
}

// deviceJIDs extracts the non-nil device JID strings for the decision function.
func deviceJIDs(devices []*store.Device) []string {
	out := make([]string, 0, len(devices))
	for _, d := range devices {
		if d.ID != nil {
			out = append(out, d.ID.String())
		}
	}
	return out
}

// bootstrapAdmin creates and pairs the admin session if the admin number is set
// and not yet paired (§6). It returns the pairing code (also logged to console)
// or "" when nothing was needed.
func (m *Manager) bootstrapAdmin(ctx context.Context, devices []*store.Device) (string, error) {
	if m.cfg.AdminNumber == "" {
		return "", nil
	}
	adminJID := types.NewJID(m.cfg.AdminNumber, types.DefaultUserServer).String()
	if !adminNeedsPairing(m.cfg.AdminNumber, deviceJIDs(devices), adminJID) {
		m.log.Info("admin number already paired; skipping bootstrap", "number", m.cfg.AdminNumber)
		return "", nil
	}

	// Find or create the is_admin_session row.
	sess, err := m.repo.GetByJID(ctx, adminJID)
	if err != nil || sess == nil {
		now := m.clock.NowMs()
		phone := m.cfg.AdminNumber
		sess = &domain.WASession{
			ID:             domain.NewSessionID(),
			TenantID:       m.cfg.AdminTenantID,
			Status:         domain.SessionStarting,
			PhoneNumber:    &phone,
			IsAdminSession: true,
			AutoRead:       m.cfg.DefaultAutoRead,
			RatePerMin:     m.cfg.DefaultRatePerMin,
			RatePerHour:    m.cfg.DefaultRatePerHour,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := m.repo.Create(ctx, sess); err != nil {
			return "", fmt.Errorf("create admin session: %w", err)
		}
	}

	// Pair via phone code on a fresh device.
	code, err := m.startPairingCode(ctx, sess, m.cfg.AdminNumber)
	if err != nil {
		return "", fmt.Errorf("admin pairing: %w", err)
	}
	// Surface the code: console + (Phase 3) admin panel via the persisted/emitted
	// auth.code event already published by startPairingCode.
	m.log.Info("ADMIN NUMBER PAIRING CODE — link in WhatsApp > Linked Devices > Link with phone number",
		"number", m.cfg.AdminNumber, "code", code)
	return code, nil
}

// ----------------------------------------------------------------------------
// Public lifecycle: Create / Start / Stop / Restart / Logout
// ----------------------------------------------------------------------------

// CreateSession persists a new (unpaired) app session row and registers a
// ManagedSession against a fresh device. It does not connect; call StartQR or
// StartPairingCode to pair.
func (m *Manager) CreateSession(ctx context.Context, tenantID string, label *string, autoRead, presenceTyping bool) (*domain.WASession, error) {
	now := m.clock.NowMs()
	sess := &domain.WASession{
		ID:             domain.NewSessionID(),
		TenantID:       tenantID,
		Label:          label,
		Status:         domain.SessionStopped,
		AutoRead:       autoRead,
		PresenceTyping: presenceTyping,
		RatePerMin:     m.cfg.DefaultRatePerMin,
		RatePerHour:    m.cfg.DefaultRatePerHour,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := m.repo.Create(ctx, sess); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	m.mu.Lock()
	m.sessions[sess.ID] = &ManagedSession{
		SessionID: sess.ID,
		TenantID:  sess.TenantID,
		IsAdmin:   sess.IsAdminSession,
		device:    m.keystore.NewDevice(),
		status:    domain.SessionStopped,
	}
	m.mu.Unlock()
	return sess, nil
}

// Get returns the ManagedSession for id, or nil if unknown.
func (m *Manager) Get(id string) *ManagedSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
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
				m.sink.Publish(ctx, domain.NewEvent(domain.EventAuthQR, ms.SessionID, ms.TenantID, map[string]any{
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
	return m.startPairingCode(ctx, sessionRow(ms), phone)
}

// sessionRow projects the minimal session identity startPairingCode needs.
func sessionRow(ms *ManagedSession) *domain.WASession {
	return &domain.WASession{ID: ms.SessionID, TenantID: ms.TenantID, IsAdminSession: ms.IsAdmin}
}

// startPairingCode is the shared pairing-code path used by both the public API
// and admin bootstrap.
func (m *Manager) startPairingCode(ctx context.Context, sess *domain.WASession, phone string) (string, error) {
	ms := m.Get(sess.ID)
	if ms == nil {
		// Admin bootstrap path: register a managed session with a fresh device.
		ms = &ManagedSession{
			SessionID: sess.ID,
			TenantID:  sess.TenantID,
			IsAdmin:   sess.IsAdminSession,
			device:    m.keystore.NewDevice(),
			status:    domain.SessionStarting,
		}
		m.mu.Lock()
		m.sessions[sess.ID] = ms
		m.mu.Unlock()
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
	code, err := client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, pairDisplayName)
	if err != nil {
		m.teardown(ms)
		m.setStatus(loopCtx, ms, domain.SessionFailed)
		return "", fmt.Errorf("pair phone: %w", err)
	}
	m.sink.Publish(loopCtx, domain.NewEvent(domain.EventAuthCode, ms.SessionID, ms.TenantID, map[string]any{
		"code":  code,
		"phone": phone,
	}))
	go m.reconnectLoopAfterPair(loopCtx, ms)
	return code, nil
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
		// Detached background context: event handlers fire outside any request.
		ctx := context.Background()
		m.applyEvent(ctx, ms, evt)
		if m.inbound != nil {
			m.inbound.Handle(ctx, ms.SessionID, ms.TenantID, ms.IsAdmin, evt)
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

// recordPairedJID persists the phone/LID JIDs onto the session row after pairing.
func (m *Manager) recordPairedJID(ctx context.Context, ms *ManagedSession, jid, lid types.JID) {
	sess, err := m.repo.Get(ctx, ms.SessionID)
	if err != nil || sess == nil {
		m.log.Warn("pair success: session row missing", "session", ms.SessionID, "err", err)
		return
	}
	j := jid.String()
	sess.WAJID = &j
	if !lid.IsEmpty() {
		l := lid.String()
		sess.WALID = &l
	}
	sess.UpdatedAt = m.clock.NowMs()
	if err := m.repo.Update(ctx, sess); err != nil {
		m.log.Warn("pair success: update session failed", "session", ms.SessionID, "err", err)
	}
}

// setStatus updates in-memory + persisted status and emits a session.status event
// — but only when the status actually changed, so we don't spam duplicates.
func (m *Manager) setStatus(ctx context.Context, ms *ManagedSession, status domain.SessionStatus) {
	ms.mu.Lock()
	if ms.status == status {
		ms.mu.Unlock()
		return
	}
	ms.status = status
	ms.mu.Unlock()

	if err := m.repo.UpdateStatus(ctx, ms.SessionID, status); err != nil {
		m.log.Warn("persist status failed", "session", ms.SessionID, "status", status, "err", err)
	}
	if m.sink != nil {
		m.sink.Publish(ctx, domain.NewEvent(domain.EventSessionStatus, ms.SessionID, ms.TenantID, map[string]any{
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
