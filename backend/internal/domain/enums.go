package domain

// String-typed enums mirroring the §5 DDL ENUM columns. They are plain string
// newtypes (not iota ints) so the wire/DB representation is the literal string
// the DDL specifies — no mapping table to drift out of sync.

// SessionStatus mirrors wa_sessions.status. §3 lists the familiar UPPERCASE
// status names (STARTING, SCAN_QR_CODE, …); the DDL stores their lowercase
// forms. These constants are the stored/wire values; FromStatusName maps the
// §3 names onto them.
type SessionStatus string

const (
	SessionStarting  SessionStatus = "starting"
	SessionScanQR    SessionStatus = "scan_qr_code"
	SessionWorking   SessionStatus = "working"
	SessionFailed    SessionStatus = "failed"
	SessionStopped   SessionStatus = "stopped"
	SessionLoggedOut SessionStatus = "logged_out"
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
// exhausted.
type WebhookDeliveryStatus string

const (
	DeliveryPending   WebhookDeliveryStatus = "pending"
	DeliveryDelivered WebhookDeliveryStatus = "delivered"
	DeliveryFailed    WebhookDeliveryStatus = "failed"
	DeliveryDead      WebhookDeliveryStatus = "dead"
)

// OutboxStatus mirrors outbox.status.
type OutboxStatus string

const (
	OutboxQueued  OutboxStatus = "queued"
	OutboxSending OutboxStatus = "sending"
	OutboxSent    OutboxStatus = "sent"
	OutboxFailed  OutboxStatus = "failed"
	OutboxUnknown OutboxStatus = "unknown"
)
