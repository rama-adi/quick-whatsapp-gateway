package wa

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/desiredstate"
)

// ----------------------------------------------------------------------------
// Consumer interfaces (Go convention: defined by the consumer). The composition
// root wires concrete types in. We depend on these small surfaces, never on
// sibling internal packages.
// ----------------------------------------------------------------------------

// Keystore is the slice of the whatsmeow device container the manager needs. It
// is satisfied by the gateway-local SQLite keystore (internal/wa/store). The
// whatsmeow store.Device type is an external type and is allowed here. See recon
// §4a.
type Keystore interface {
	// GetAllDevices loads every persisted device on boot.
	GetAllDevices(ctx context.Context) ([]*store.Device, error)
	// GetFirstDevice returns the first device, or a fresh NewDevice() (never nil,
	// no error) when the keystore is empty.
	GetFirstDevice(ctx context.Context) (*store.Device, error)
	// GetDevice returns the device for a JID, or nil if absent.
	GetDevice(ctx context.Context, jid types.JID) (*store.Device, error)
	// NewDevice mints an unpersisted device (ID == nil) for a fresh pairing.
	NewDevice() *store.Device
	// DeleteDevice removes a device from the keystore (used on logout).
	DeleteDevice(ctx context.Context, device *store.Device) error
}

// EventSink publishes a domain.Event onto the eventing fabric (Redis pub/sub +
// webhook enqueue + event_log). The manager only emits session.status, auth.qr
// and auth.code; inbound message events flow through InboundHandler instead.
type EventSink interface {
	Publish(ctx context.Context, evt domain.Event)
}

// InboundHandler receives every raw whatsmeow event for a session, tagged with
// the app session/organization ids and whether this is the admin session. The inbound
// pipeline (internal/wa/inbound) implements it; the manager only forwards.
type InboundHandler interface {
	Handle(ctx context.Context, sessionID, organizationID string, isAdmin bool, evt any)
}

// Clock abstracts time for deterministic tests.
type Clock interface {
	NowMs() int64
}

// realClock is the production Clock.
type realClock struct{}

func (realClock) NowMs() int64 { return domain.NowMs() }

// waClient is the slice of *whatsmeow.Client the session wrapper drives. The
// concrete *whatsmeow.Client satisfies it; tests use a fake. Keeping this narrow
// lets the per-session lifecycle be exercised without a real WebSocket.
// Client is the lifecycle network boundary accepted by Manager.SetClientFactory.
// A simulated device can implement it while retaining the real manager lifecycle.
type Client = waClient

type waClient interface {
	Connect() error
	Disconnect()
	IsConnected() bool
	IsLoggedIn() bool
	Logout(ctx context.Context) error
	AddEventHandler(handler whatsmeow.EventHandler) uint32
	// RemoveEventHandler unregisters a handler. It must not be called from
	// inside an event handler (whatsmeow holds the handler list's read lock
	// while dispatching).
	RemoveEventHandler(id uint32) bool
	SendPasskeyResponse(ctx context.Context, response *types.WebAuthnResponse) error
	SendPasskeyConfirmation(ctx context.Context) error
	PairPhone(
		ctx context.Context,
		phone string,
		showPushNotification bool,
		clientType whatsmeow.PairClientType,
		clientDisplayName string,
	) (string, error)
	SendPresence(ctx context.Context, state types.Presence) error
	SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error
	MarkRead(
		ctx context.Context,
		ids []types.MessageID,
		timestamp time.Time,
		chat types.JID,
		sender types.JID,
		receiptTypeExtra ...types.ReceiptType,
	) error
}

// compile-time assertion that the real client satisfies the interface.
var _ waClient = (*whatsmeow.Client)(nil)

// ----------------------------------------------------------------------------
// connection — one run of a session.
// ----------------------------------------------------------------------------

// connection is one run of a session: one whatsmeow client, its registered
// event handler and the goroutines that serve it (reconnect driver, QR emitter,
// passkey deadline). gen identifies the run within its session. Every callback
// carries the connection it belongs to, and a callback whose connection is no
// longer the session's current one is dropped, so a late event from a torn
// down client can never change the state of a newer run.
type connection struct {
	gen       uint64
	client    waClient
	handlerID uint32
	// ctx ends when the run ends (stop, terminal event, shutdown).
	ctx    context.Context
	cancel context.CancelFunc
	// wake tells the reconnect driver that the transport dropped.
	wake chan struct{}
	// ready is closed after the first successful Connect of this run, so a
	// phone-number pairing request can wait until the socket is up.
	ready     chan struct{}
	readyOnce sync.Once
	// loggedIn is closed on the first Connected event (the device is
	// authenticated), so a logout of a stopped session can wait for it.
	loggedIn     chan struct{}
	loggedInOnce sync.Once
	// qrRotate carries ADV-secret rotations to the running QR emitter.
	qrRotate chan *events.RotateADVSecret

	// Guarded by the owning ManagedSession's mu.
	qrCancel context.CancelFunc // stops the current QR emitter
	down     bool               // transport dropped and not yet reconnected
	downSeq  uint64             // counts drops, so a grace timer only acts on its own drop
}

// newConnection builds run gen of a session around client. The caller
// registers the event handler and starts the reconnect driver.
func newConnection(gen uint64, client waClient) *connection {
	ctx, cancel := context.WithCancel(context.Background())
	return &connection{
		gen:      gen,
		client:   client,
		ctx:      ctx,
		cancel:   cancel,
		wake:     make(chan struct{}, 1),
		ready:    make(chan struct{}),
		loggedIn: make(chan struct{}),
		qrRotate: make(chan *events.RotateADVSecret, 4),
	}
}

func (c *connection) markReady()    { c.readyOnce.Do(func() { close(c.ready) }) }
func (c *connection) markLoggedIn() { c.loggedInOnce.Do(func() { close(c.loggedIn) }) }

// signalDrop wakes the reconnect driver without blocking.
func (c *connection) signalDrop() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// ----------------------------------------------------------------------------
// ManagedSession — one app session and its lifecycle state.
// ----------------------------------------------------------------------------

// ManagedSession is the lifecycle capsule for exactly one app session.
// SessionID and OrganizationID are immutable after registration.
//
// Two locks with different jobs guard it:
//
//   - op serializes lifecycle operations (start, stop, pairing, logout,
//     forget). An operation holds it for its whole duration, so two operations
//     never interleave their read-then-act steps. Event callbacks never take
//     it; they identify their run through its connection instead.
//   - mu guards every field below it and is held only for short reads and
//     writes, never across network calls.
//
// The latest QR is only an in-memory pairing aid with an expiry, not durable
// authentication state; the device keystore remains the source of truth after
// PairSuccess.
type ManagedSession struct {
	SessionID      string
	OrganizationID string
	IsAdmin        bool

	op sync.Mutex

	mu             sync.Mutex
	assignedConfig *desiredstate.Config
	device         *store.Device
	conn           *connection // the current run; nil when not running
	nextGen        uint64      // last connection generation issued

	status    domain.SessionStatus
	failure   domain.SessionFailureReason // set only while status is failed
	seq       uint64                      // status_seq of the current status (0 = never changed)
	changedAt int64                       // epoch-ms of the last status change
	attempt   int                         // consecutive failed connection attempts in this run
	lastErr   string                      // the last connection error of this run

	// generation is the start generation of the current or last run; latched
	// is set when that run ended terminally (failed, logged out). A latched
	// session starts again only for a newer generation, i.e. when a user
	// asked again; heartbeat snapshots alone never restart it.
	generation uint64
	latched    bool

	lastQR        string // most recent QR code streamed during pairing (for GET /qr)
	lastQRExpires int64  // epoch-ms when lastQR stops being valid (0 = unknown)

	// passkey is non-nil while WhatsApp waits for passkey verification of this
	// pairing. QR codes stop while it is pending; the connection stays open
	// until it completes, fails, or passes its deadline.
	passkey *pendingPasskey

	pairedJID string // device JID observed at PairSuccess ("" until paired)
	pairedLID string // LID observed at PairSuccess ("" until known)
}

// Status returns the current status under lock.
func (s *ManagedSession) Status() domain.SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// LatestQR returns the most recently streamed QR code and its expiry (epoch-ms).
// code is "" when no QR is currently available (the session is not in QR pairing).
func (s *ManagedSession) LatestQR() (code string, expiresAt int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastQR, s.lastQRExpires
}

// runningClient returns the client of the current run (nil when not running)
// and the session's device.
func (s *ManagedSession) runningClient() (waClient, *store.Device) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil, s.device
	}
	return s.conn.client, s.device
}

// current reports whether c is still the session's running connection.
func (s *ManagedSession) current(c *connection) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return c != nil && s.conn == c
}

// ----------------------------------------------------------------------------
// Status state machine (pure). Maps whatsmeow events to a status + whether the
// reconnect loop should continue. This is the heart of §3 and is unit-tested
// directly without any client.
// ----------------------------------------------------------------------------

// transition is the result of classifying a whatsmeow event.
type transition struct {
	// status is the new status; "" means "no status change for this event".
	status domain.SessionStatus
	// changed reports whether status carries a meaningful value.
	changed bool
	// reason says why, when status is failed. It is "" for every other status.
	reason domain.SessionFailureReason
	// terminal reports whether this event ends the run (LoggedOut,
	// StreamReplaced, ban, fatal connect failure, pairing failure).
	terminal bool
}

// classifyEvent maps a whatsmeow event to a status transition per §3:
//
//   - Connected            -> WORKING, reset backoff, keep running.
//   - Disconnected         -> (transient) no status change; the reconnect driver
//     retries and reports reconnecting only after a grace period.
//   - LoggedOut            -> LOGGED_OUT, STOP reconnect (terminal).
//   - StreamReplaced       -> FAILED, STOP reconnect (terminal).
//   - TemporaryBan         -> FAILED, STOP reconnect (terminal).
//   - ClientOutdated       -> FAILED, STOP reconnect (terminal).
//   - ConnectFailure       -> FAILED for fatal reasons (logged-out/banned/locked/
//     outdated/bad-UA) STOP; otherwise transient (no status change, keep retrying).
//   - PairSuccess          -> STARTING (handshake done, awaiting Connected).
//   - PairError            -> FAILED(pairing_error), STOP (terminal).
//   - PairPasskeyError     -> FAILED(passkey_failed), STOP (terminal).
//   - PairPasskeyRequest/Confirmation, QR -> the pairing driver (pairing.go).
//   - QR/QRScanned...       -> handled by the QR pump, not here.
//
// Events not relevant to status return changed=false, terminal=false.
func classifyEvent(evt any) transition {
	switch e := evt.(type) {
	case *events.Connected:
		return transition{status: domain.SessionWorking, changed: true}
	case *events.PairSuccess:
		// Pairing handshake completed; the lib reconnects and a Connected follows.
		return transition{status: domain.SessionStarting, changed: true}
	case *events.LoggedOut:
		return transition{status: domain.SessionLoggedOut, changed: true, terminal: true}
	case *events.StreamReplaced:
		return failedTransition(domain.FailureStreamReplaced)
	case *events.TemporaryBan:
		return failedTransition(domain.FailureTemporaryBan)
	case *events.ClientOutdated:
		return failedTransition(domain.FailureClientOutdated)
	case *events.PairError:
		return failedTransition(domain.FailurePairingError)
	case *events.PairPasskeyError:
		return failedTransition(domain.FailurePasskeyFailed)
	case *events.ConnectFailure:
		if isFatalConnectFailure(e.Reason) {
			return failedTransition(connectFailureReason(e.Reason))
		}
		// Transient failure: let the reconnect loop keep trying.
		return transition{}
	default:
		return transition{}
	}
}

// failedTransition is the terminal FAILED transition for reason.
func failedTransition(reason domain.SessionFailureReason) transition {
	return transition{status: domain.SessionFailed, reason: reason, changed: true, terminal: true}
}

// connectFailureReason maps a fatal ConnectFailure onto the failure reason the
// API and UI show.
func connectFailureReason(r events.ConnectFailureReason) domain.SessionFailureReason {
	switch r {
	case events.ConnectFailureTempBanned:
		return domain.FailureTemporaryBan
	case events.ConnectFailureClientOutdated:
		return domain.FailureClientOutdated
	default:
		return domain.FailureConnectRejected
	}
}

// isFatalConnectFailure reports whether a ConnectFailure reason is permanent and
// must stop the reconnect loop (vs a transient server hiccup worth retrying).
func isFatalConnectFailure(r events.ConnectFailureReason) bool {
	switch r {
	case events.ConnectFailureLoggedOut,
		events.ConnectFailureTempBanned,
		events.ConnectFailureMainDeviceGone,
		events.ConnectFailureUnknownLogout,
		events.ConnectFailureClientOutdated,
		events.ConnectFailureBadUserAgent:
		return true
	default:
		return false
	}
}

// ----------------------------------------------------------------------------
// Backoff schedule (pure). Exponential with full jitter, capped. Deterministic
// given an injected *rand.Rand, so the schedule is unit-tested exactly.
// ----------------------------------------------------------------------------

// backoffConfig parameterizes the reconnect schedule.
type backoffConfig struct {
	base   time.Duration // delay for attempt 0
	max    time.Duration // ceiling
	factor float64       // growth per attempt (e.g. 2.0)
}

// defaultBackoff is the production reconnect schedule: 1s base, x2, capped 2m.
var defaultBackoff = backoffConfig{base: time.Second, max: 2 * time.Minute, factor: 2.0}

// backoffFor returns the delay before reconnect attempt n (0-based) using full
// jitter: sleep = random in [0, min(max, base*factor^n)]. With a seeded *rand.Rand
// the result is deterministic. rng must not be nil.
func backoffFor(cfg backoffConfig, attempt int, rng *rand.Rand) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	// Compute base*factor^attempt in float to avoid intermediate overflow, then
	// clamp to max before converting back to a Duration.
	d := float64(cfg.base)
	for range attempt {
		d *= cfg.factor
		if d >= float64(cfg.max) {
			d = float64(cfg.max)
			break
		}
	}
	if d > float64(cfg.max) {
		d = float64(cfg.max)
	}
	// Full jitter: uniform in [0, d].
	return time.Duration(rng.Int63n(int64(d) + 1))
}

// pendingPasskey is one passkey verification WhatsApp requested during pairing.
type pendingPasskey struct {
	options   *types.WebAuthnPublicKey
	expiresAt int64 // epoch-ms; the pairing fails with passkey_failed after it
	// confirmCode is set once WhatsApp asks the user to compare a code with the
	// phone ("" until then).
	confirmCode string
}
