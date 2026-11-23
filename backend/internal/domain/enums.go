package domain

// String-typed enums mirroring the §5 DDL ENUM columns. They are plain string
// newtypes (not iota ints) so the wire/DB representation is the literal string
// the DDL specifies — no mapping table to drift out of sync.

// SessionStatus is a session's observed runtime status. The gateway reports
// every value except unavailable, which the API derives at read time; the
// observed values are stored in wa_session_runtime.status. §3 lists the
// familiar UPPERCASE status names (STARTING, SCAN_QR_CODE, …); these constants
// are their lowercase stored/wire values.
type SessionStatus string

const (
	SessionStarting  SessionStatus = "starting"
	SessionScanQR    SessionStatus = "scan_qr_code"
	SessionWorking   SessionStatus = "working"
	SessionFailed    SessionStatus = "failed"
	SessionStopped   SessionStatus = "stopped"
	SessionLoggedOut SessionStatus = "logged_out"
	// SessionReconnecting: the connection dropped and is still down after the
	// gateway's grace period; the gateway keeps retrying with backoff.
	SessionReconnecting SessionStatus = "reconnecting"
	// SessionUnavailable is never reported by a gateway: the API reads it for
	// a session that should be live (starting, pairing, working, reconnecting)
	// while its gateway is not connected, so its real state is unknown.
	SessionUnavailable SessionStatus = "unavailable"
)

// Observed reports whether s is a status a gateway may report (every status
// except the API-derived unavailable).
func (s SessionStatus) Observed() bool {
	switch s {
	case SessionStarting, SessionScanQR, SessionWorking, SessionReconnecting,
		SessionFailed, SessionStopped, SessionLoggedOut:
		return true
	default:
		return false
	}
}

// Live reports whether s describes a session with an active connection or
// connection attempt, whose truth therefore depends on its gateway being up.
func (s SessionStatus) Live() bool {
	switch s {
	case SessionStarting, SessionScanQR, SessionWorking, SessionReconnecting:
		return true
	default:
		return false
	}
}

// DesiredState is what the session's users asked for: run or stop. It is
// written only by user actions (create with start, start, stop, restart,
// logout, pairing-code request) and is the only input the desired-state
// snapshot reads besides pairing identity and settings.
type DesiredState string

const (
	DesiredRun  DesiredState = "run"
	DesiredStop DesiredState = "stop"
)

// SessionFailureReason says why a session is in the failed status. It is set
// only while status is failed and cleared on every other status, so a reader
// never sees a stale reason next to a healthy status.
type SessionFailureReason string

const (
	// FailurePasskeyFailed: WhatsApp asked for the account's passkey during
	// linking and the verification failed or was not finished in time.
	FailurePasskeyFailed SessionFailureReason = "passkey_failed"
	// FailureQRTimeout: every QR code expired without a successful scan.
	FailureQRTimeout SessionFailureReason = "qr_timeout"
	// FailurePairingError: WhatsApp rejected or aborted the linking handshake.
	FailurePairingError SessionFailureReason = "pairing_error"
	// FailureClientOutdated: WhatsApp rejected the gateway's client version.
	FailureClientOutdated SessionFailureReason = "client_outdated"
	// FailureStreamReplaced: another client took over this device's connection.
	FailureStreamReplaced SessionFailureReason = "stream_replaced"
	// FailureTemporaryBan: WhatsApp temporarily banned the account.
	FailureTemporaryBan SessionFailureReason = "temporary_ban"
	// FailureConnectRejected: WhatsApp refused the connection for a permanent reason.
	FailureConnectRejected SessionFailureReason = "connect_rejected"
	// FailureConnectError: the gateway could not open a connection to WhatsApp.
	FailureConnectError SessionFailureReason = "connect_error"
)

// Valid reports whether r is one of the known failure reasons.
func (r SessionFailureReason) Valid() bool {
	switch r {
	case FailurePasskeyFailed, FailureQRTimeout, FailurePairingError, FailureClientOutdated,
		FailureStreamReplaced, FailureTemporaryBan, FailureConnectRejected, FailureConnectError:
		return true
	default:
		return false
	}
}

// statusNameMap maps the §3 UPPERCASE familiar names onto stored values.
var statusNameMap = map[string]SessionStatus{
	"STARTING":     SessionStarting,
	"SCAN_QR_CODE": SessionScanQR,
	"WORKING":      SessionWorking,
	"FAILED":       SessionFailed,
	"STOPPED":      SessionStopped,
	"LOGGED_OUT":   SessionLoggedOut,
	"RECONNECTING": SessionReconnecting,
	"UNAVAILABLE":  SessionUnavailable,
}

// SessionStatusFromName resolves a §3 familiar status name (e.g. "WORKING")
// to its stored SessionStatus. ok is false for unknown names.
func SessionStatusFromName(name string) (s SessionStatus, ok bool) {
	s, ok = statusNameMap[name]
	return
}

// GatewayStatus mirrors gateways.status (router accounting Layer 1, D8). The
// lifecycle is joining → active → draining → drained; unreachable is derived by
// the router from a stale heartbeat (it is not written by the gateway's own boot
// path). Only `active` gateways receive new session placements and proxied
// traffic; the router 503s requests bound for a non-active/stale gateway.
type GatewayStatus string

const (
	GatewayPendingEnrollment GatewayStatus = "pending_enrollment"
	GatewayJoining           GatewayStatus = "joining"
	GatewayActive            GatewayStatus = "active"
	GatewayDraining          GatewayStatus = "draining"
	GatewayDrained           GatewayStatus = "drained"
	GatewayDegraded          GatewayStatus = "degraded"
	GatewayDisabled          GatewayStatus = "disabled"
	GatewayUnreachable       GatewayStatus = "unreachable" // derived API state; never persisted
)

func (s GatewayStatus) Valid() bool {
	switch s {
	case GatewayPendingEnrollment, GatewayJoining, GatewayActive,
		GatewayDraining, GatewayDrained, GatewayDegraded, GatewayDisabled:
		return true
	default:
		return false
	}
}

// ChatType mirrors chats.type.
type ChatType string

const (
	ChatDM         ChatType = "dm"
	ChatGroup      ChatType = "group"
	ChatNewsletter ChatType = "newsletter"
	ChatBroadcast  ChatType = "broadcast"
	ChatStatus     ChatType = "status"
)

// MessageDirection mirrors messages.direction.
type MessageDirection string

const (
	DirectionIn  MessageDirection = "in"
	DirectionOut MessageDirection = "out"
)

// MessageOrigin records where a stored message row came from (messages.origin).
type MessageOrigin string

const (
	// OriginLive is a message captured from a live WhatsApp event or our own send.
	OriginLive MessageOrigin = "live"
	// OriginHistory is a message imported from WhatsApp history sync on (re)link.
	OriginHistory MessageOrigin = "history"
	// OriginBackup is a message imported from a WhatsApp backup file.
	OriginBackup MessageOrigin = "backup"
)

// MessageStatus mirrors messages.status.
type MessageStatus string

const (
	MessagePending   MessageStatus = "pending"
	MessageSent      MessageStatus = "sent"
	MessageDelivered MessageStatus = "delivered"
	MessageRead      MessageStatus = "read"
	MessagePlayed    MessageStatus = "played"
	MessageFailed    MessageStatus = "failed"
	MessageUnknown   MessageStatus = "unknown"
)

// GroupRole mirrors whatsapp_group_members.role.
type GroupRole string

const (
	RoleMember     GroupRole = "member"
	RoleAdmin      GroupRole = "admin"
	RoleSuperAdmin GroupRole = "superadmin"
)

// WebhookDeliveryStatus mirrors webhook_deliveries.status. "dead" = retries
// exhausted; "delivering" = claimed by a dispatcher with a live lease.
type WebhookDeliveryStatus string

const (
	DeliveryPending    WebhookDeliveryStatus = "pending"
	DeliveryDelivering WebhookDeliveryStatus = "delivering"
	DeliveryDelivered  WebhookDeliveryStatus = "delivered"
	DeliveryFailed     WebhookDeliveryStatus = "failed"
	DeliveryDead       WebhookDeliveryStatus = "dead"
)

// OutboxStatus mirrors outbox.status.
type OutboxStatus string

const (
	OutboxQueued OutboxStatus = "queued"
	// OutboxSending is the leased state: a claim token and lease name the
	// scheduler that owns the command.
	OutboxSending OutboxStatus = "sending"
	// OutboxAcknowledged means WhatsApp accepted the send and its projection
	// (messages row, sent event) is waiting for a retry.
	OutboxAcknowledged OutboxStatus = "acknowledged"
	OutboxSent         OutboxStatus = "sent"
	OutboxFailed       OutboxStatus = "failed"
	OutboxUnknown      OutboxStatus = "unknown"
)
