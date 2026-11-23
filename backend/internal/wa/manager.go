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
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/socket"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/desiredstate"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

// Reconnect defaults. See Config for what each one controls.
const (
	defaultReconnectGrace     = 10 * time.Second
	defaultMaxConnectAttempts = 10
	defaultLivenessInterval   = 5 * time.Second
	// keepAliveMaxFail matches whatsmeow's KeepAliveMaxFailTime: with
	// whatsmeow's own reconnect disabled, the manager forces the reconnect.
	keepAliveMaxFail = 3 * time.Minute
)

// Config holds the manager's tunables, populated from ENV by the composition root.
type Config struct {
	// GatewayID identifies this engine and supplies the default linked-device name.
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
	// Backoff overrides the reconnect schedule; the zero value uses defaultBackoff.
	Backoff backoffConfig
	// ReconnectGrace is how long a dropped connection may stay down before the
	// session reports it is reconnecting. WhatsApp drops connections several
	// times a day and they come back within a second or two; reporting each
	// drop would look like an outage to users and webhooks. Zero uses 10s.
	ReconnectGrace time.Duration
	// MaxConnectAttempts is how many consecutive connection attempts may fail
	// before the session fails with connect_error. Zero uses 10.
	MaxConnectAttempts int
	// LivenessInterval is how often the reconnect driver checks that a
	// connected client still has its socket, to catch drops that emit no
	// Disconnected event. Zero uses 5s.
	LivenessInterval time.Duration
	// HistoryFullSync asks the phone for its full chat history (not only the
	// recent part) when a device is linked. Applied at registration only.
	HistoryFullSync bool
	// HistoryFullSyncMaxMB caps the full-sync size the phone may send.
	HistoryFullSyncMaxMB uint32
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
// Each started session runs as one connection (see connection): a client, its
// event handler and one reconnect driver. The manager is the only reconnect
// driver; whatsmeow's own auto-reconnect is turned off for every client it
// builds. The manager is also the only producer of session.status events.
type Manager struct {
	keystore Keystore
	sink     EventSink
	inbound  InboundHandler
	history  atomic.Pointer[HistoryInbox]
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
// log/clock may be nil (sensible defaults are used).
func NewManager(
	keystore Keystore,
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
	if cfg.InboundEventTimeout <= 0 {
		cfg.InboundEventTimeout = 10 * time.Second
	}
	if cfg.PresenceTimeout <= 0 {
		cfg.PresenceTimeout = 10 * time.Second
	}
	if cfg.ReconnectGrace <= 0 {
		cfg.ReconnectGrace = defaultReconnectGrace
	}
	if cfg.MaxConnectAttempts <= 0 {
		cfg.MaxConnectAttempts = defaultMaxConnectAttempts
	}
	if cfg.LivenessInterval <= 0 {
		cfg.LivenessInterval = defaultLivenessInterval
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
// organization during boot/reconciliation. A session that is already running
// keeps its connection; only its settings refresh.
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
		ms = &ManagedSession{
			SessionID:      assignment.SessionID,
			OrganizationID: assignment.OrganizationID,
			device:         device,
			status:         domain.SessionStopped,
		}
		m.sessions[assignment.SessionID] = ms
	}
	m.mu.Unlock()

	ms.op.Lock()
	defer ms.op.Unlock()
	config := assignment.Config
	ms.mu.Lock()
	ms.assignedConfig = &config
	running := ms.conn != nil
	ms.mu.Unlock()
	if !running {
		m.beginRun(ms)
	}
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
	ms.op.Lock()
	defer ms.op.Unlock()
	m.stopRun(ms)
	m.setState(ctx, ms, nil, domain.SessionStopped, "")
	return nil
}

// ----------------------------------------------------------------------------
// Registry
// ----------------------------------------------------------------------------

// Get returns the ManagedSession for id, or nil when unknown.
func (m *Manager) Get(id string) *ManagedSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// EnsureDevice registers a ManagedSession with a fresh keystore device for an
// API-created session row, without inserting anything into wa_sessions — the
// API owns rows. Idempotent: an already-known session is left untouched (its
// existing device, if any, stays).
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
// separately because a working status can briefly outlive a lost transport
// connection (during the reconnect grace period).
func (m *Manager) ConnectionState(id string) (status domain.SessionStatus, connected, loggedIn, found bool) {
	ms := m.Get(id)
	if ms == nil {
		return "", false, false, false
	}
	ms.mu.Lock()
	status = ms.status
	var client waClient
	if ms.conn != nil {
		client = ms.conn.client
	}
	ms.mu.Unlock()
	if client != nil {
		connected = client.IsConnected()
		loggedIn = client.IsLoggedIn()
	}
	return status, connected, loggedIn, true
}

// readyClient returns the client of a session that is connected and working.
// Anything else is a temporary condition reported as session_not_ready (503):
// the caller may retry once the session is working again.
func (m *Manager) readyClient(id string) (waClient, error) {
	ms := m.Get(id)
	if ms == nil {
		return nil, domain.ErrNotFound("session not found")
	}
	ms.mu.Lock()
	status := ms.status
	var client waClient
	if ms.conn != nil {
		client = ms.conn.client
	}
	ms.mu.Unlock()
	if client == nil || status != domain.SessionWorking {
		if status == "" {
			status = domain.SessionStopped
		}
		return nil, domain.ErrUnavailable("session is not ready: its status is " + string(status)).
			WithDetails(map[string]any{"reason": "session_not_ready", "status": string(status)})
	}
	return client, nil
}

// OutboundClientFor returns the per-session protocol builder with the matching
// network transport. A fake session keeps whatsmeow's local payload builders
// while sending every external operation to its configured fake server.
func (m *Manager) OutboundClientFor(id string) (outbound.WAClient, bool) {
	ms := m.Get(id)
	if ms == nil {
		return nil, false
	}
	ms.mu.Lock()
	var c waClient
	if ms.conn != nil {
		c = ms.conn.client
	}
	ms.mu.Unlock()
	if c == nil {
		return nil, false
	}
	if cli, ok := c.(*whatsmeow.Client); ok {
		return outbound.NewWhatsmeowClient(cli), true
	}
	if remote, ok := c.(interface {
		LocalClient() *whatsmeow.Client
		Transport() outbound.WhatsAppTransport
	}); ok {
		return outbound.NewWhatsmeowClientWithTransport(remote.LocalClient(), remote.Transport()), true
	}
	return nil, false
}

// Forget tears down a session's runtime and drops it from the in-memory
// registry. It does NOT touch the wa_sessions row or the keystore device — the
// caller (the API delete path) owns those. Safe to call for an unknown id.
func (m *Manager) Forget(id string) {
	ms := m.Get(id)
	if ms == nil {
		return
	}
	ms.op.Lock()
	m.stopRun(ms)
	m.mu.Lock()
	if m.sessions[id] == ms {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	ms.op.Unlock()
	if inbox := m.historyInbox(); inbox != nil {
		inbox.Discard(context.Background(), id, "session removed")
	}
}

// LatestQR returns the most recently streamed QR code for a session and its
// expiry (epoch-ms); code is "" when the session is unknown or no QR is
// currently available. It lets callers avoid a Get+nil dance on the runtime.
func (m *Manager) LatestQR(id string) (code string, expiresAt int64) {
	ms := m.Get(id)
	if ms == nil {
		return "", 0
	}
	code, expiresAt = ms.LatestQR()
	if expiresAt <= m.clock.NowMs() {
		return "", 0
	}
	return code, expiresAt
}

// ----------------------------------------------------------------------------
// Runs: start, stop, end
// ----------------------------------------------------------------------------

// beginRun starts a new connection for the session and its reconnect driver.
// The caller holds ms.op and has checked that no run is active.
func (m *Manager) beginRun(ms *ManagedSession) *connection {
	ms.mu.Lock()
	if ms.device == nil {
		ms.device = m.keystore.NewDevice()
	}
	ms.nextGen++
	c := newConnection(ms.nextGen, m.buildClient(ms.device))
	c.handlerID = c.client.AddEventHandler(m.eventHandlerFor(ms, c))
	ms.conn = c
	ms.attempt, ms.lastErr = 0, ""
	ms.lastQR, ms.lastQRExpires = "", 0
	ms.passkey = nil
	event, changed := m.stateLocked(ms, domain.SessionStarting, "")
	ms.mu.Unlock()
	if changed {
		m.publish(c.ctx, event)
	}
	go m.drive(ms, c)
	return c
}

// stopRun ends the current run, if any, without changing the status. The
// caller holds ms.op and records the resulting status itself.
func (m *Manager) stopRun(ms *ManagedSession) {
	ms.mu.Lock()
	c := ms.conn
	ms.conn = nil
	ms.lastQR, ms.lastQRExpires = "", 0
	ms.passkey = nil
	ms.mu.Unlock()
	if c != nil {
		m.closeConn(c)
	}
}

// endRun ends run c because of a terminal outcome and records status. It does
// nothing when c is no longer the current run, so a late outcome of an old
// run cannot end a newer one. fromHandler must be true when called from inside
// a whatsmeow event handler: the client is then closed asynchronously, since
// whatsmeow forbids removing a handler while it dispatches.
func (m *Manager) endRun(
	ctx context.Context,
	ms *ManagedSession,
	c *connection,
	status domain.SessionStatus,
	reason domain.SessionFailureReason,
	fromHandler bool,
) bool {
	ms.mu.Lock()
	if c == nil || ms.conn != c {
		ms.mu.Unlock()
		return false
	}
	ms.conn = nil
	ms.lastQR, ms.lastQRExpires = "", 0
	ms.passkey = nil
	if status == domain.SessionLoggedOut {
		m.resetDeviceLocked(ms)
	}
	event, changed := m.stateLocked(ms, status, reason)
	ms.mu.Unlock()

	if fromHandler {
		c.cancel()
		go m.closeConn(c)
	} else {
		m.closeConn(c)
	}
	if status == domain.SessionLoggedOut {
		m.discardHistory(ctx, ms)
		// Republished even when unchanged: the API projection repeats its
		// identity clear, repairing rows left stale by older versions.
		changed = true
	}
	if changed {
		m.publish(ctx, event)
	}
	return true
}

// closeConn ends a run's goroutines, unregisters its event handler and closes
// its socket. It must not run inside that client's event handler.
func (m *Manager) closeConn(c *connection) {
	c.cancel()
	c.client.RemoveEventHandler(c.handlerID)
	c.client.Disconnect()
}

// resetDeviceLocked swaps a logged-out device for a fresh unpaired one. Call
// with ms.mu held.
func (m *Manager) resetDeviceLocked(ms *ManagedSession) {
	ms.device = m.keystore.NewDevice()
	ms.pairedJID, ms.pairedLID = "", ""
}

func (m *Manager) discardHistory(ctx context.Context, ms *ManagedSession) {
	if inbox := m.historyInbox(); inbox != nil {
		inbox.Discard(ctx, ms.SessionID, "session logged out")
	}
}

// ----------------------------------------------------------------------------
// Reconnect driver
// ----------------------------------------------------------------------------

// drive owns run c's connection lifetime: it connects, waits for the transport
// to drop, and reconnects with backoff. It is the only code that calls Connect
// for the run; whatsmeow's own auto-reconnect is disabled (buildClient).
// Consecutive failures without a Connected event in between are counted; the
// run fails with connect_error after MaxConnectAttempts, or with
// connect_rejected at once when WhatsApp refuses the connection permanently.
func (m *Manager) drive(ms *ManagedSession, c *connection) {
	rng := newSeededRand()
	liveness := time.NewTicker(m.cfg.LivenessInterval)
	defer liveness.Stop()
	for c.ctx.Err() == nil {
		err := c.client.Connect()
		if c.ctx.Err() != nil {
			return
		}
		// A socket that is already up (for example after whatsmeow's own
		// post-pairing reconnect) is a successful connection, not a failure.
		if err == nil || errors.Is(err, whatsmeow.ErrAlreadyConnected) {
			c.markReady()
			if !m.awaitDrop(ms, c, liveness) {
				return
			}
			err = errors.New("connection dropped")
		}
		attempt, ok := m.countFailure(ms, c, err)
		if !ok {
			return
		}
		if !retryableConnectError(err) {
			m.log.Warn("whatsapp refused the connection", "session", ms.SessionID, "err", err)
			m.endRun(c.ctx, ms, c, domain.SessionFailed, domain.FailureConnectRejected, false)
			return
		}
		if attempt >= m.cfg.MaxConnectAttempts {
			m.log.Warn("whatsapp connection attempts exhausted", "session", ms.SessionID, "attempts", attempt, "err", err)
			m.endRun(c.ctx, ms, c, domain.SessionFailed, domain.FailureConnectError, false)
			return
		}
		delay := backoffFor(m.cfg.Backoff, attempt-1, rng)
		m.log.Debug("reconnect wait", "session", ms.SessionID, "attempt", attempt, "delay", delay, "err", err)
		timer := time.NewTimer(delay)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// awaitDrop blocks while run c's transport is up. It returns true when the
// transport dropped (a Disconnected event, a forced keepalive reconnect, or a
// liveness check finding no socket) and false when the run ended.
func (m *Manager) awaitDrop(ms *ManagedSession, c *connection, liveness *time.Ticker) bool {
	for {
		select {
		case <-c.ctx.Done():
			return false
		case <-c.wake:
		case <-liveness.C:
			if c.client.IsConnected() {
				continue
			}
		}
		m.markDown(ms, c)
		return true
	}
}

// countFailure records one failed attempt of run c. ok is false when c is no
// longer the current run.
func (m *Manager) countFailure(ms *ManagedSession, c *connection, err error) (attempt int, ok bool) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.conn != c {
		return 0, false
	}
	ms.attempt++
	ms.lastErr = err.Error()
	if !c.down {
		// A first connect that never came up counts as down from now on, so
		// the grace period also covers a run that cannot connect at all.
		m.markDownLocked(ms, c)
	}
	return ms.attempt, true
}

// markDown records that run c's transport dropped and arms the grace timer.
func (m *Manager) markDown(ms *ManagedSession, c *connection) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.conn == c && !c.down {
		m.markDownLocked(ms, c)
	}
}

// markDownLocked marks run c as down and reports reconnecting if it is still
// down after the grace period. Call with ms.mu held.
func (m *Manager) markDownLocked(ms *ManagedSession, c *connection) {
	c.down = true
	c.downSeq++
	drop := c.downSeq
	time.AfterFunc(m.cfg.ReconnectGrace, func() { m.reportReconnecting(ms, c, drop) })
}

// reportReconnecting publishes that run c is reconnecting, if it is still the
// current run and still down after the grace period.
func (m *Manager) reportReconnecting(ms *ManagedSession, c *connection, drop uint64) {
	ms.mu.Lock()
	if ms.conn != c || !c.down || c.downSeq != drop || ms.status == domain.SessionStarting {
		ms.mu.Unlock()
		return
	}
	event, changed := m.stateLocked(ms, domain.SessionStarting, "")
	attempt, lastErr := ms.attempt, ms.lastErr
	ms.mu.Unlock()
	if changed {
		m.log.Info("whatsapp connection still down after grace period", "session", ms.SessionID,
			"attempt", attempt, "err", lastErr, "grace", m.cfg.ReconnectGrace.String())
		m.publish(c.ctx, event)
	}
}

// retryableConnectError reports whether a failed Connect is worth retrying.
// HTTP status errors other than timeouts and server errors, and a deleted
// device, mean WhatsApp will keep refusing; everything else (network errors,
// dial failures, handshake timeouts, a dropped socket) is transient.
func retryableConnectError(err error) bool {
	if errors.Is(err, store.ErrDeviceDeleted) {
		return false
	}
	var status socket.ErrWithStatusCode
	if errors.As(err, &status) {
		switch status.StatusCode {
		case 408, 500, 501, 502, 503, 504:
			return true
		default:
			return false
		}
	}
	return true
}

// ----------------------------------------------------------------------------
// Pairing (§6, recon §3)
// ----------------------------------------------------------------------------

// StartQR begins QR pairing for an unpaired session. The pairing driver
// (pairing.go) publishes each QR code as an auth.qr event, moves the status to
// SCAN_QR_CODE once a code exists, and runs a passkey verification if WhatsApp
// asks for one. Repeating it while the pairing runs is idempotent.
func (m *Manager) StartQR(ctx context.Context, id string) error {
	ms := m.Get(id)
	if ms == nil {
		return domain.ErrNotFound("session not found")
	}
	ms.op.Lock()
	defer ms.op.Unlock()
	ms.mu.Lock()
	running := ms.conn != nil
	unpaired := ms.device == nil || ms.device.ID == nil
	status := ms.status
	ms.mu.Unlock()
	if running {
		if !unpaired {
			return domain.ErrConflict("session already running")
		}
		// Replay the observed state even when unchanged, repairing API/UI
		// state after a missed event.
		m.publish(ctx, domain.NewEvent(domain.EventSessionStatus, ms.SessionID, ms.OrganizationID, map[string]any{
			"status": string(status),
		}))
		return nil
	}
	if !unpaired {
		return domain.ErrConflict("session is already paired")
	}
	m.beginRun(ms)
	return nil
}

// StartPairingCode begins phone-number pairing and returns the linking code
// (recon §3). When a QR pairing already runs for the unpaired device, the code
// is requested on that same connection: whatsmeow's PairPhone runs on a
// connected pairing client, which also receives QR codes. Otherwise it starts
// the pairing run itself and waits for its socket before asking.
func (m *Manager) StartPairingCode(ctx context.Context, id, phone string) (string, error) {
	ms := m.Get(id)
	if ms == nil {
		return "", domain.ErrNotFound("session not found")
	}
	ms.op.Lock()
	defer ms.op.Unlock()
	ms.mu.Lock()
	c := ms.conn
	paired := ms.device != nil && ms.device.ID != nil
	ms.mu.Unlock()
	if paired {
		return "", domain.ErrConflict("session is already paired")
	}
	started := false
	if c == nil {
		c = m.beginRun(ms)
		started = true
	}
	select {
	case <-c.ready:
	case <-c.ctx.Done():
		return "", domain.ErrConflict("pairing ended before WhatsApp was reached")
	case <-ctx.Done():
		if started {
			m.endRun(context.WithoutCancel(ctx), ms, c, domain.SessionFailed, domain.FailureConnectError, false)
		}
		return "", fmt.Errorf("connect for pairing: %w", ctx.Err())
	}
	code, err := c.client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, pairDisplayName())
	if err != nil {
		if started {
			// This call opened the connection only to ask for a code; a
			// rejected request ends it. A running QR flow keeps going, so
			// the user can still scan.
			m.endRun(context.WithoutCancel(ctx), ms, c, domain.SessionFailed, domain.FailurePairingError, false)
		}
		return "", fmt.Errorf("pair phone: %w", err)
	}
	publishCtx := context.WithoutCancel(ctx)
	// The session waits for the user to enter the code on the phone.
	m.setState(publishCtx, ms, c, domain.SessionScanQR, "")
	m.publish(publishCtx, domain.NewEvent(domain.EventAuthCode, ms.SessionID, ms.OrganizationID, map[string]any{
		"code":  code,
		"phone": phone,
	}))
	return code, nil
}

// pairDisplayName is the companion name sent with a phone-number pairing
// request. WhatsApp validates it as "Browser (OS)" with a common browser and OS
// and answers 400 bad-request for anything else, so it cannot carry the
// configured device name (e.g. "Chrome (Linux - gw-1)" is rejected). The name in
// the phone's Linked devices list comes from store.SetOSInfo instead.
func pairDisplayName() string {
	return "Chrome (Linux)"
}

// ----------------------------------------------------------------------------
// Logout
// ----------------------------------------------------------------------------

// Logout unlinks the device from WhatsApp (server-side), deletes its keystore
// device and marks the session LOGGED_OUT. Unlinking needs a logged-in
// connection: a stopped paired session is connected for the duration of the
// call. When WhatsApp does not confirm the unlink the call fails and the
// device stays paired, so the caller can retry; reporting success there would
// leave a linked device behind on the phone. An unpaired session has nothing
// to unlink and is simply marked logged out.
func (m *Manager) Logout(ctx context.Context, id string) error {
	ms := m.Get(id)
	if ms == nil {
		return domain.ErrNotFound("session not found")
	}
	ms.op.Lock()
	defer ms.op.Unlock()
	ms.mu.Lock()
	c := ms.conn
	device := ms.device
	paired := device != nil && device.ID != nil
	ms.mu.Unlock()

	publishCtx := context.WithoutCancel(ctx)
	if !paired {
		m.stopRun(ms)
		ms.mu.Lock()
		m.resetDeviceLocked(ms)
		event, _ := m.stateLocked(ms, domain.SessionLoggedOut, "")
		ms.mu.Unlock()
		m.discardHistory(publishCtx, ms)
		m.publish(publishCtx, event)
		return nil
	}

	started := false
	if c == nil {
		c = m.beginRun(ms)
		started = true
	}
	select {
	case <-c.loggedIn:
	case <-c.ctx.Done():
		return domain.ErrConflict("the session could not connect to WhatsApp to unlink the device")
	case <-ctx.Done():
		if started {
			m.stopRun(ms)
			m.setState(publishCtx, ms, nil, domain.SessionStopped, "")
		}
		return fmt.Errorf("connect for logout: %w", ctx.Err())
	}
	if err := c.client.Logout(ctx); err != nil {
		if started {
			m.stopRun(ms)
			m.setState(publishCtx, ms, nil, domain.SessionStopped, "")
		}
		m.log.Warn("whatsapp did not confirm the logout", "session", id, "err", err)
		return fmt.Errorf("unlink device: %w", err)
	}
	if err := m.keystore.DeleteDevice(ctx, device); err != nil {
		// whatsmeow already deletes the device after a confirmed logout;
		// this only cleans up what it left.
		m.log.Debug("delete device after logout", "session", id, "err", err)
	}
	m.endRun(publishCtx, ms, c, domain.SessionLoggedOut, "", false)
	return nil
}

// ----------------------------------------------------------------------------
// Event handling
// ----------------------------------------------------------------------------

// eventHandlerFor returns the whatsmeow EventHandler for run c of a session: it
// runs the status state machine, then forwards every event to the inbound
// handler. Lifecycle handling is skipped for events of a run that is no longer
// current; their data still reaches the inbound pipeline, whose sink checks
// ownership, so a message that arrived just before a stop is not lost.
func (m *Manager) eventHandlerFor(ms *ManagedSession, c *connection) whatsmeow.EventHandler {
	return func(evt any) {
		// Event handlers fire outside any request, but their database and WhatsApp
		// work must still yield resources if a dependency stalls. Keep the callback
		// synchronous for per-client ordering while bounding its lifetime.
		ctx, cancel := context.WithTimeout(context.Background(), m.cfg.InboundEventTimeout)
		defer cancel()
		// History-sync notifications go to the durable inbox, never through
		// the live inbound pipeline (no auto-read, interceptors or events).
		if msg, notif, ok := historyNotification(evt); ok {
			m.acceptHistory(ctx, ms, c, msg, notif)
			return
		}
		if ms.current(c) {
			// QR rotation and the passkey flow are lifecycle work, not inbound data.
			if m.handlePairingEvent(ms, c, evt) {
				return
			}
			m.applyEvent(ctx, ms, c, evt)
		} else if isLifecycleEvent(evt) {
			return
		}
		m.mu.RLock()
		inbound := m.inbound
		m.mu.RUnlock()
		if inbound != nil {
			inbound.Handle(ctx, ms.SessionID, ms.OrganizationID, ms.IsAdmin, evt)
		}
	}
}

// isLifecycleEvent reports whether evt only describes the connection, so a
// stale run's copy carries nothing for the inbound pipeline.
func isLifecycleEvent(evt any) bool {
	switch evt.(type) {
	case *events.Connected, *events.Disconnected, *events.LoggedOut, *events.StreamReplaced,
		*events.TemporaryBan, *events.ClientOutdated, *events.ConnectFailure, *events.KeepAliveTimeout,
		*events.KeepAliveRestored, *events.PairSuccess, *events.PairError, *events.PairPasskeyError,
		*events.PairPasskeyRequest, *events.PairPasskeyConfirmation, *events.QR, *events.RotateADVSecret,
		*events.QRScannedWithoutMultidevice:
		return true
	}
	return false
}

// applyEvent runs the status state machine for one event of the current run
// c and, on terminal events, ends the run.
func (m *Manager) applyEvent(ctx context.Context, ms *ManagedSession, c *connection, evt any) {
	switch e := evt.(type) {
	case *events.PairSuccess:
		// Capture the JID the moment pairing succeeds.
		m.recordPairedJID(ms, c, e.ID, e.LID)
		if inbox := m.historyInbox(); inbox != nil && !e.ID.IsEmpty() {
			inbox.Paired(ctx, ms.SessionID, e.ID.ToNonAD().String())
		}
	case *events.Connected:
		ms.mu.Lock()
		if ms.conn == c {
			ms.attempt, ms.lastErr = 0, ""
			c.down = false
		}
		ms.mu.Unlock()
		c.markReady()
		c.markLoggedIn()
		// Announce "available". On a freshly-paired session the push name has not
		// synced yet, so SendPresence no-ops with ErrNoPushName — the
		// AppStateSyncComplete case below re-announces once the push name lands.
		m.sendOnlinePresence(ms, c)
	case *events.AppStateSyncComplete:
		// SendPresence requires the push name, which only arrives via app-state
		// sync — that completes AFTER Connected. (Re)announce availability when a
		// sync lands so the account actually shows online after pairing.
		m.sendOnlinePresence(ms, c)
	case *events.Disconnected:
		c.signalDrop()
	case *events.KeepAliveTimeout:
		// whatsmeow only forces a reconnect on failing keepalives when its own
		// auto-reconnect is on; the manager owns reconnects, so it does it.
		if !e.LastSuccess.IsZero() && time.Since(e.LastSuccess) > keepAliveMaxFail {
			m.log.Warn("keepalive failing; forcing a reconnect", "session", ms.SessionID, "errors", e.ErrorCount)
			go func() {
				c.client.Disconnect()
				c.signalDrop()
			}()
		}
	}

	t := classifyEvent(evt)
	if t.terminal {
		m.log.Warn("whatsapp session terminated", "session", ms.SessionID, "event", fmt.Sprintf("%T", evt),
			"detail", fmt.Sprintf("%+v", evt), "status", string(t.status), "reason", string(t.reason))
		m.endRun(ctx, ms, c, t.status, t.reason, true)
		return
	}
	if t.changed {
		m.setState(ctx, ms, c, t.status, t.reason)
	}
}

// sendOnlinePresence announces "available" for run c in the background.
// A missing push name (expected right after Connected, before app-state sync has
// delivered it) is downgraded to debug — applyEvent re-announces once the
// AppStateSyncComplete event fires, which is when presence actually sticks.
func (m *Manager) sendOnlinePresence(ms *ManagedSession, c *connection) {
	go func() {
		ctx, cancel := context.WithTimeout(c.ctx, m.cfg.PresenceTimeout)
		defer cancel()
		err := c.client.SendPresence(ctx, types.PresenceAvailable)
		switch {
		case err == nil:
		case errors.Is(err, whatsmeow.ErrNoPushName):
			m.log.Debug("online presence deferred until push name syncs", "session", ms.SessionID)
		case c.ctx.Err() != nil:
		default:
			m.log.Warn("send online presence failed", "session", ms.SessionID, "err", err)
		}
	}()
}

// recordPairedJID records the paired phone/LID JIDs on the managed session so
// live-ops and telemetry can read them. Persistence is API-owned: the session
// row is updated from the committed auth.code event, not by the gateway.
func (m *Manager) recordPairedJID(ms *ManagedSession, c *connection, jid, lid types.JID) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.conn != c {
		return
	}
	ms.passkey = nil
	ms.lastQR, ms.lastQRExpires = "", 0
	if c.qrCancel != nil {
		c.qrCancel()
		c.qrCancel = nil
	}
	ms.pairedJID = jid.String()
	if !lid.IsEmpty() {
		ms.pairedLID = lid.String()
	}
}

// ----------------------------------------------------------------------------
// Status
// ----------------------------------------------------------------------------

// setState records status for run c and publishes a session.status event when
// the status or failure reason changed. c == nil records a status that does
// not belong to a run (stopped by an operation); otherwise nothing happens
// when c is no longer the current run. It reports whether the state applied.
func (m *Manager) setState(
	ctx context.Context,
	ms *ManagedSession,
	c *connection,
	status domain.SessionStatus,
	reason domain.SessionFailureReason,
) bool {
	ms.mu.Lock()
	if c != nil && ms.conn != c {
		ms.mu.Unlock()
		return false
	}
	event, changed := m.stateLocked(ms, status, reason)
	ms.mu.Unlock()
	if changed {
		m.publish(ctx, event)
	}
	return true
}

// stateLocked applies status and builds its session.status event. changed is
// false when neither the status nor the failure reason changed. The reason is
// kept only for the failed status. Call with ms.mu held; publish the event
// after releasing it.
func (m *Manager) stateLocked(
	ms *ManagedSession,
	status domain.SessionStatus,
	reason domain.SessionFailureReason,
) (domain.Event, bool) {
	if status != domain.SessionFailed {
		reason = ""
	}
	changed := ms.status != status || ms.failure != reason
	ms.status = status
	ms.failure = reason
	payload := map[string]any{"status": string(status)}
	if reason != "" {
		payload["failureReason"] = string(reason)
	}
	return domain.NewEvent(domain.EventSessionStatus, ms.SessionID, ms.OrganizationID, payload), changed
}

// publish hands an event to the sink. Failure paths end the run first, which
// cancels its context; publishing with it would drop the event, so the
// publish is detached from cancellation.
func (m *Manager) publish(ctx context.Context, event domain.Event) {
	if m.sink != nil {
		m.sink.Publish(context.WithoutCancel(ctx), event)
	}
}

// ----------------------------------------------------------------------------
// Shutdown
// ----------------------------------------------------------------------------

// Shutdown disconnects every session and stops their runs. It does not change
// any status; the next authoritative snapshot determines what resumes.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.RLock()
	all := make([]*ManagedSession, 0, len(m.sessions))
	for _, ms := range m.sessions {
		all = append(all, ms)
	}
	m.mu.RUnlock()

	for _, ms := range all {
		ms.op.Lock()
		m.stopRun(ms)
		ms.op.Unlock()
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
