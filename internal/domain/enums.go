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
	GatewayJoining     GatewayStatus = "joining"
	GatewayActive      GatewayStatus = "active"
	GatewayDraining    GatewayStatus = "draining"
	GatewayDrained     GatewayStatus = "drained"
	GatewayUnreachable GatewayStatus = "unreachable"
)

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

// MessageStatus mirrors messages.status.
type MessageStatus string

const (
	MessagePending   MessageStatus = "pending"
	MessageSent      MessageStatus = "sent"
	MessageDelivered MessageStatus = "delivered"
	MessageRead      MessageStatus = "read"
	MessagePlayed    MessageStatus = "played"
	MessageFailed    MessageStatus = "failed"
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
)
