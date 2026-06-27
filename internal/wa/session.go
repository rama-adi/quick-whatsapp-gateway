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

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
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

// SessionRepo is the slice of the wa_sessions repository the manager calls. It
// is satisfied by the MySQL repo in internal/store.
type SessionRepo interface {
	Get(ctx context.Context, id string) (*domain.WASession, error)
	GetByJID(ctx context.Context, jid string) (*domain.WASession, error)
	ListByOrg(ctx context.Context, organizationID string) ([]*domain.WASession, error)
	Create(ctx context.Context, s *domain.WASession) error
	Update(ctx context.Context, s *domain.WASession) error
	// UpdateStatus is a narrow fast-path used on every status transition; it also
	// stamps last_connected_at when status becomes WORKING.
	UpdateStatus(ctx context.Context, id string, status domain.SessionStatus) error
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
type waClient interface {
	Connect() error
	Disconnect()
	IsConnected() bool
	IsLoggedIn() bool
	Logout(ctx context.Context) error
	AddEventHandler(handler whatsmeow.EventHandler) uint32
	GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error)
	PairPhone(ctx context.Context, phone string, showPushNotification bool, clientType whatsmeow.PairClientType, clientDisplayName string) (string, error)
	SendPresence(ctx context.Context, state types.Presence) error
	SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error
	MarkRead(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID, receiptTypeExtra ...types.ReceiptType) error
}

// compile-time assertion that the real client satisfies the interface.
var _ waClient = (*whatsmeow.Client)(nil)

// ----------------------------------------------------------------------------
// ManagedSession — one whatsmeow client + its lifecycle state.
// ----------------------------------------------------------------------------

// ManagedSession wraps a single whatsmeow client with its app-level identity,
// current status, reconnect bookkeeping and a cancellable goroutine context.
type ManagedSession struct {
	SessionID      string
	OrganizationID string
	IsAdmin        bool

	device *store.Device
	client waClient

	mu        sync.Mutex
	status    domain.SessionStatus
	attempt   int                // consecutive reconnect attempts (resets on Connected)
	reconnect bool               // whether the reconnect loop should keep running
	cancel    context.CancelFunc // cancels the per-session goroutine context
	handlerID uint32             // whatsmeow event-handler registration id

	lastQR        string // most recent QR code streamed during pairing (for GET /qr)
	lastQRExpires int64  // epoch-ms when lastQR stops being valid (0 = unknown)
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
	// keepReconnect reports whether the reconnect loop should continue after this
	// event. It is only consulted when terminal is true.
	keepReconnect bool
	// terminal reports whether this event decides the reconnect-loop fate
	// (LoggedOut / StreamReplaced / ban / fatal connect-failure stop it).
	terminal bool
}

// classifyEvent maps a whatsmeow event to a status transition per §3:
//
//   - Connected            -> WORKING, reset backoff, keep running.
//   - Disconnected         -> (transient) no status change; reconnect loop drives retries.
//   - LoggedOut            -> LOGGED_OUT, STOP reconnect (terminal).
//   - StreamReplaced       -> FAILED, STOP reconnect (terminal).
//   - TemporaryBan         -> FAILED, STOP reconnect (terminal).
//   - ClientOutdated       -> FAILED, STOP reconnect (terminal).
//   - ConnectFailure       -> FAILED for fatal reasons (logged-out/banned/locked/
//     outdated/bad-UA) STOP; otherwise transient (no status change, keep retrying).
//   - PairSuccess          -> STARTING (handshake done, awaiting Connected).
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
		return transition{status: domain.SessionLoggedOut, changed: true, terminal: true, keepReconnect: false}
	case *events.StreamReplaced:
		return transition{status: domain.SessionFailed, changed: true, terminal: true, keepReconnect: false}
	case *events.TemporaryBan:
		return transition{status: domain.SessionFailed, changed: true, terminal: true, keepReconnect: false}
	case *events.ClientOutdated:
		return transition{status: domain.SessionFailed, changed: true, terminal: true, keepReconnect: false}
	case *events.ConnectFailure:
		if isFatalConnectFailure(e.Reason) {
			return transition{status: domain.SessionFailed, changed: true, terminal: true, keepReconnect: false}
		}
		// Transient failure: let the reconnect loop keep trying.
		return transition{}
	default:
		return transition{}
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
	for i := 0; i < attempt; i++ {
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
