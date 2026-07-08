package domain

import "encoding/json"

// One Go struct per §5 table. Conventions:
//   - Timestamps are epoch-ms int64 (BIGINT in the DDL).
//   - NULLable columns are pointers (*string/*int64/…) so a nil value round-trips
//     as SQL NULL and serializes as JSON null/omitted.
//   - Opaque JSON columns use json.RawMessage; JSON columns with an obvious shape
//     use a typed struct (Permissions, RetryPolicy, MediaMeta).
//   - JSON tags use the API's camelCase conventions (§11) for fields that are
//     serialized over the wire.

// Gateway mirrors the gateways registry table (§7, router accounting Layer 1). The
// registry is the routing table the central router reads: each row pins where a
// gateway is reachable (BaseURL), whether it is taking traffic (Status), and how
// loaded it is (SessionCount/Capacity) so the router can place new sessions on the
// least-loaded reachable gateway and 503 requests bound for an unreachable one.
type Gateway struct {
	ID           string        `json:"id" doc:"The gateway's id (equals the gateway's configured GATEWAY_ID). Stable for the life of the gateway instance." example:"gw-sg-1"`
	Label        *string       `json:"label,omitempty" doc:"Human-friendly name for the gateway, for dashboards. Optional; null when unset." example:"Singapore primary"`
	Status       GatewayStatus `json:"status" enum:"joining,active,draining,drained,unreachable" doc:"Gateway lifecycle state, used by the central router to decide placement and proxying. Lifecycle: **joining** (registered, not yet taking traffic) → **active** (healthy and accepting new session placements + proxied traffic) → **draining** (finishing existing work, no new placements) → **drained** (idle, safe to retire). **unreachable** is derived by the router from a stale heartbeat (not written by the gateway itself). Only **active** gateways receive new sessions and proxied requests; requests bound for a non-active or stale gateway are answered 503." example:"active"`
	SessionCount int           `json:"sessionCount" doc:"Number of sessions currently pinned to this gateway. Used by the router to place new sessions on the least-loaded reachable gateway." example:"12"`
	Capacity     *int          `json:"capacity,omitempty" doc:"Soft placement cap: the max sessions the router will place here. Optional; null means unbounded." example:"100"`
	BaseURL      *string       `json:"baseUrl,omitempty" doc:"The gateway's public base URL, where the router and clients reach it. Optional; null until the gateway advertises one." example:"https://gw-sg-1.example.com"`
	LastSeenAt   *int64        `json:"lastSeenAt,omitempty" doc:"When the gateway last sent a heartbeat, in epoch milliseconds (UTC). Optional; null if it has never reported. A stale value is what makes the router treat the gateway as unreachable." example:"1719662400000"`
	CreatedAt    int64         `json:"createdAt" doc:"When the gateway row was first registered, in epoch milliseconds (UTC)." example:"1719662400000"`
	UpdatedAt    int64         `json:"updatedAt" doc:"When the gateway row was last updated, in epoch milliseconds (UTC)." example:"1719662400000"`
}

// WASession mirrors the wa_sessions table (an attached WhatsApp number).
type WASession struct {
	ID              string        `json:"id" doc:"The session id (a ULID). Stable identifier for the attached WhatsApp number; used in all per-session routes." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	OrganizationID  string        `json:"organizationId" doc:"Id of the better-auth organization that owns this session. All access to the session is scoped to this org." example:"org_01J9ABC..."`
	CreatedByUserID *string       `json:"createdByUserId,omitempty" doc:"Id of the better-auth user who created the session, kept for audit. Optional; null when created by a non-user principal (e.g. an API key) or unknown." example:"user_01J9DEF..."`
	GatewayID       string        `json:"gatewayId" doc:"Id of the gateway that holds this session's WhatsApp keystore and live connection. The session is pinned here; a dashboard uses it (and the embedded gateway object on responses that include it) to show where the session runs." example:"gw-sg-1"`
	Label           *string       `json:"label,omitempty" doc:"Human-friendly name for the session. Optional; null when unset." example:"Support line"`
	Status          SessionStatus `json:"status" enum:"starting,scan_qr_code,working,failed,stopped,logged_out" doc:"Where the session is in its lifecycle. **starting** = connecting (also used after a transient disconnect, since the gateway auto-reconnects); **scan_qr_code** = waiting for the pairing QR code to be scanned (fetch it via the QR endpoint); **working** = connected and usable for sending/receiving; **failed** = the connection broke (e.g. the number was linked to another device); **stopped** = deliberately stopped by an operator; **logged_out** = unpaired on the phone, requires a fresh QR scan to recover. Typical pairing path: starting → scan_qr_code → working." example:"working"`
	WAJID           *string       `json:"waJid,omitempty" doc:"The session's own WhatsApp JID, set once the number is paired. Optional; null before pairing." example:"6281234567890@s.whatsapp.net"`
	WALID           *string       `json:"waLid,omitempty" doc:"The session's linked-device id (LID) — WhatsApp's privacy-preserving per-device address. Optional; null before pairing." example:"205227043110953@lid"`
	PhoneNumber     *string       `json:"phoneNumber,omitempty" doc:"The paired phone number in plain digits (E.164 without +). Optional; null before pairing." example:"6281234567890"`
	IsAdminSession  bool          `json:"isAdminSession" doc:"True if this is the gateway's configured admin session (the WHATSAPP_ADMIN_NUMBER). The admin session has extra privileges such as triggering live-data backfills." example:"false"`
	AutoRead        bool          `json:"autoRead" doc:"Whether incoming messages are automatically marked read (blue ticks) before the session acts on them." example:"false"`
	PresenceTyping  bool          `json:"presenceTyping" doc:"Whether a \"typing…\" presence indicator is sent to the recipient while a message is being sent." example:"true"`
	RatePerMin      int           `json:"ratePerMin" doc:"Outbound send rate limit for this session, in messages per minute. Sends exceeding the limit are throttled." example:"20"`
	RatePerHour     int           `json:"ratePerHour" doc:"Outbound send rate limit for this session, in messages per hour. Sends exceeding the limit are throttled." example:"600"`
	LastConnectedAt *int64        `json:"lastConnectedAt,omitempty" doc:"When the session last established a live WhatsApp connection, in epoch milliseconds (UTC). Optional; null if it has never connected." example:"1719662400000"`
	CreatedAt       int64         `json:"createdAt" doc:"When the session was created, in epoch milliseconds (UTC)." example:"1719662400000"`
	UpdatedAt       int64         `json:"updatedAt" doc:"When the session was last updated, in epoch milliseconds (UTC)." example:"1719662400000"`
}

// PresenceStatus is the snapshot returned by the live presence lookup endpoint.
// WhatsApp only pushes contact presence after a subscription; the initial GET may
// therefore return state="unknown" and a later presence.update event carries the
// available/unavailable transition.
type PresenceStatus struct {
	ChatJID     string `json:"chatJid,omitempty" doc:"Chat the presence applies to, for chat (typing) presence." example:"6281234567890@s.whatsapp.net"`
	From        string `json:"from" doc:"JID whose presence was requested or changed." example:"6281234567890@s.whatsapp.net"`
	State       string `json:"state" doc:"Presence state: unknown until WhatsApp emits a subscribed update, then available (online), unavailable (offline), composing, or paused." enum:"unknown,available,unavailable,composing,paused" example:"available"`
	Media       string `json:"media,omitempty" doc:"For chat presence, the kind being composed: text or audio." enum:"text,audio"`
	Unavailable bool   `json:"unavailable,omitempty" doc:"True when the contact went offline." example:"false"`
	LastSeen    int64  `json:"lastSeen,omitempty" doc:"Last-seen time in epoch milliseconds, when the contact shares it." example:"1719662400000"`
}

// Permissions is the typed shape of api_keys.permissions JSON.
type Permissions struct {
	Read   bool `json:"read" doc:"Read scope: the key may read resources (sessions, chats, messages, contacts, groups). Required for all GET-style endpoints." example:"true"`
	Send   bool `json:"send" doc:"Send scope: the key may send outbound messages and perform message actions (mark read, react, etc.)." example:"true"`
	Manage bool `json:"manage" doc:"Manage scope: the key may create/modify/delete configuration (sessions, webhooks, group settings, backfills)." example:"false"`
	Events bool `json:"events" doc:"Events scope: the key may subscribe to the realtime event stream." example:"true"`
}

// APIKey is the gateway's read-only view of a row in better-auth's `apikey` table
// (§4.2/§7). The frontend's better-auth api-key plugin is the sole writer; the
// gateway only verifies presented keys against this row and resolves the owning
// organization. Keys are org-scoped: a key acts within exactly one organization.
//
// KeyHash holds better-auth's stored hash of the key (column `key`) and is never
// serialized. The full key is shown once at creation time by the frontend, never
// by the gateway. Permissions are decoded from better-auth's `permissions` JSON.
type APIKey struct {
	ID             string      `json:"id" doc:"The api-key's id (better-auth apikey row id). Identifies the key without exposing its secret." example:"key_01J9..."`
	Name           string      `json:"name" doc:"Human-friendly name for the key, set at creation." example:"CI pipeline"`
	KeyHash        string      `json:"-"`                                                                                                                                                                                    // better-auth stored hash (column `key`); never serialized
	UserID         *string     `json:"userId,omitempty" doc:"Id of the better-auth user that created/owns the key, for audit. Optional; null when not attributable to a user." example:"user_01J9DEF..."`                    //
	OrganizationID string      `json:"organizationId" doc:"Id of the organization the key acts within. Keys are org-scoped: a key operates in exactly one organization, resolved on verification." example:"org_01J9ABC..."` //
	Enabled        bool        `json:"enabled" doc:"Whether the key is active. A disabled key is rejected on verification." example:"true"`
	Permissions    Permissions `json:"permissions" doc:"The scopes granted to this key (read/send/manage/events). Each request is checked against the scope its endpoint requires."`
	LastUsedAt     *int64      `json:"lastUsedAt,omitempty" doc:"When the key was last used to authenticate, in epoch milliseconds (UTC). Optional; null if never used." example:"1719662400000"`
	ExpiresAt      *int64      `json:"expiresAt,omitempty" doc:"When the key expires, in epoch milliseconds (UTC). Optional; null means it never expires. After this time the key is rejected." example:"1751198400000"`
	CreatedAt      int64       `json:"createdAt" doc:"When the key was created, in epoch milliseconds (UTC)." example:"1719662400000"`
}

type OAuthClient struct {
	ID                string          `json:"id"`
	ClientID          string          `json:"clientId"`
	OrganizationID    string          `json:"organizationId"`
	CreatedByUserID   *string         `json:"createdByUserId,omitempty"`
	SessionID         string          `json:"sessionId"`
	Name              string          `json:"name"`
	LogoURL           *string         `json:"logoUrl,omitempty"`
	ClientType        string          `json:"clientType"`
	LoginCommand      string          `json:"loginCommand"`
	SecretHash        []byte          `json:"-"`
	SecretLast4       *string         `json:"secretLast4,omitempty"`
	RedirectURIs      json.RawMessage `json:"redirectUris"`
	Modes             string          `json:"modes"`
	GroupJID          *string         `json:"groupJid,omitempty"`
	AllowedScopes     json.RawMessage `json:"allowedScopes"`
	TokenTTLSeconds   int             `json:"tokenTtlSeconds"`
	RefreshTTLSeconds int             `json:"refreshTtlSeconds"`
	Status            string          `json:"status"`
	CreatedAt         int64           `json:"createdAt"`
	UpdatedAt         int64           `json:"updatedAt"`
	DeletedAt         *int64          `json:"deletedAt,omitempty"`
}

type OAuthGrant struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organizationId"`
	ClientID       string          `json:"clientId"`
	WAIdentityID   uint64          `json:"waIdentityId"`
	Sub            string          `json:"sub"`
	GrantedScopes  json.RawMessage `json:"grantedScopes"`
	LastACR        string          `json:"lastAcr"`
	LastGroupJID   *string         `json:"lastGroupJid,omitempty"`
	CreatedAt      int64           `json:"createdAt"`
	LastUsedAt     int64           `json:"lastUsedAt"`
	RevokedAt      *int64          `json:"revokedAt,omitempty"`
}

type OAuthRefreshToken struct {
	ID             string          `json:"id"`
	GrantID        string          `json:"grantId"`
	OrganizationID string          `json:"organizationId"`
	TokenHash      []byte          `json:"-"`
	FamilyID       string          `json:"familyId"`
	ParentID       *string         `json:"parentId,omitempty"`
	Scopes         json.RawMessage `json:"scopes"`
	IssuedAt       int64           `json:"issuedAt"`
	ExpiresAt      int64           `json:"expiresAt"`
	ConsumedAt     *int64          `json:"consumedAt,omitempty"`
	RevokedAt      *int64          `json:"revokedAt,omitempty"`
}

type OAuthRefreshRotation struct {
	TokenHash       []byte
	ClientID        string
	RequestedScopes []string
	Now             int64
	Successor       OAuthRefreshToken
}

type OAuthSigningKey struct {
	KID        string          `json:"kid"`
	Alg        string          `json:"alg"`
	PublicJWK  json.RawMessage `json:"publicJwk"`
	PrivateEnc []byte          `json:"-"`
	Status     string          `json:"status"`
	CreatedAt  int64           `json:"createdAt"`
	RetiredAt  *int64          `json:"retiredAt,omitempty"`
}

// RetryPolicy is the typed shape of webhooks.retry_policy JSON.
type RetryPolicy struct {
	Policy       string `json:"policy" doc:"Backoff strategy for failed deliveries. **exponential** doubles the delay each attempt (delaySeconds × 2^(attempt-1)); any other value uses a constant delay of delaySeconds." example:"exponential"`
	DelaySeconds int    `json:"delaySeconds" doc:"Base delay before retrying, in seconds. For the exponential policy this is the first-attempt delay and the base of the doubling. Defaults to 2." example:"2"`
	Attempts     int    `json:"attempts" doc:"Maximum number of delivery attempts before the delivery is given up and marked dead (dead-lettered). Defaults to 15." example:"15"`
}

// Webhook mirrors the webhooks table. HMACSecret is stored AES-GCM encrypted at
// rest and never serialized.
type Webhook struct {
	ID             string            `json:"id" doc:"The webhook's id." example:"wh_01J9..."`
	OrganizationID string            `json:"organizationId" doc:"Id of the organization that owns this webhook." example:"org_01J9ABC..."`
	SessionID      *string           `json:"sessionId,omitempty" doc:"The session this webhook is scoped to: only events from this session are delivered. Optional; null means all of the organization's sessions." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	URL            string            `json:"url" doc:"The HTTPS endpoint that receives event POSTs (the Event envelope as the request body)." example:"https://example.com/webhooks/wa"`
	Events         []string          `json:"events" doc:"Event types delivered to this webhook. Use [\"*\"] to receive every event; an empty list receives none. Example values: \"message\", \"session.status\", \"poll.vote\", \"group.update\"." example:"[\"message\",\"poll.vote\"]"`
	HMACSecret     []byte            `json:"-"` // AES-GCM encrypted at rest; never serialized (the plaintext secret is only accepted on create/update, never returned)
	CustomHeaders  map[string]string `json:"customHeaders,omitempty" doc:"Extra HTTP headers sent with each delivery. Applied last, so they can override the gateway's default headers. Optional." example:"{\"X-Tenant\":\"acme\"}"`
	RetryPolicy    RetryPolicy       `json:"retryPolicy" doc:"How failed deliveries to this webhook are retried (backoff strategy, base delay, max attempts)."`
	Active         bool              `json:"active" doc:"Whether the webhook is enabled. An inactive webhook receives no deliveries." example:"true"`
	CreatedAt      int64             `json:"createdAt" doc:"When the webhook was created, in epoch milliseconds (UTC)." example:"1719662400000"`
	UpdatedAt      int64             `json:"updatedAt" doc:"When the webhook was last updated, in epoch milliseconds (UTC)." example:"1719662400000"`
}

// WebhookDelivery mirrors the webhook_deliveries table.
type WebhookDelivery struct {
	ID           uint64                `json:"id" doc:"The delivery attempt-record id (monotonic)." example:"4821"`
	WebhookID    string                `json:"webhookId" doc:"Id of the webhook this delivery belongs to." example:"wh_01J9..."`
	EventID      string                `json:"eventId" doc:"Id of the event being delivered (the Event envelope's id). Echoed to the receiver so duplicate redeliveries can be dropped." example:"evt_01J9..."`
	Status       WebhookDeliveryStatus `json:"status" enum:"pending,delivered,failed,dead" doc:"Delivery state. **pending** = queued, not yet attempted (or awaiting a scheduled retry); **delivered** = the receiver returned a 2xx; **failed** = the last attempt failed and a retry is still scheduled (see nextRetryAt); **dead** = all retries exhausted, the delivery is given up. Progression: pending → delivered, or pending → failed → … → dead." example:"delivered"`
	Attempts     int                   `json:"attempts" doc:"How many delivery attempts have been made so far." example:"1"`
	ResponseCode *int                  `json:"responseCode,omitempty" doc:"HTTP status code returned by the receiver on the last attempt. Optional; null if no response was received (e.g. connection error/timeout)." example:"200"`
	NextRetryAt  *int64                `json:"nextRetryAt,omitempty" doc:"When the next retry is scheduled, in epoch milliseconds (UTC). Optional; null when delivered or dead (no further retries)." example:"1719662460000"`
	LastError    *string               `json:"lastError,omitempty" doc:"Error message from the last failed attempt (HTTP error or transport failure). Optional; null when the latest attempt succeeded." example:"received status 503"`
	CreatedAt    int64                 `json:"createdAt" doc:"When the delivery record was created, in epoch milliseconds (UTC)." example:"1719662400000"`
}

// Identity mirrors the whatsapp_identities table — global LID->phone/name
// resolution. Push name lives here; nickname is group-specific (see GroupMember).
type Identity struct {
	ID           uint64  `json:"id" doc:"The identity row id (internal numeric key)." example:"1024"`
	LID          string  `json:"lid" doc:"The person's linked-device id (LID) — WhatsApp's privacy-preserving address and the stable key for this identity." example:"205227043110953@lid"`
	PhoneNumber  *string `json:"phoneNumber,omitempty" doc:"The person's phone number in plain digits, when resolved. Optional; null when only the LID is known." example:"6281234567890"`
	PhoneJID     *string `json:"phoneJid,omitempty" doc:"The person's phone-based WhatsApp JID, when resolved. Optional; null when only the LID is known." example:"6281234567890@s.whatsapp.net"`
	Name         *string `json:"name,omitempty" doc:"The person's push name (their self-set display name) — the preferred display value. Optional; null when unknown." example:"Alice"`
	BusinessName *string `json:"businessName,omitempty" doc:"The verified business name, for WhatsApp Business accounts. Optional; null for non-business accounts." example:"Acme Corp"`
	FirstSeenAt  int64   `json:"firstSeenAt" doc:"When this identity was first observed, in epoch milliseconds (UTC)." example:"1719662400000"`
	UpdatedAt    int64   `json:"updatedAt" doc:"When this identity was last updated, in epoch milliseconds (UTC)." example:"1719662400000"`
}

// Contact mirrors the whatsapp_contacts table — a per-account "found user"
// record powering the Contacts feature (where this session encountered them).
// Contact is the "found users" projection over the central identity table: a
// person a session has encountered, with where we found them. It is not a stored
// row — it is assembled from whatsapp_identities plus per-session DM (chats) and
// group-membership (whatsapp_group_members) signals.
type Contact struct {
	// ID is the underlying identity id, exposed only for cursor pagination.
	ID           uint64  `json:"-"`
	LID          string  `json:"lid" doc:"The contact's linked-device id (LID) — WhatsApp's privacy-preserving address, used as the stable key for a contact." example:"205227043110953@lid"`
	PhoneNumber  *string `json:"phoneNumber,omitempty" doc:"The contact's phone number in plain digits, when known. Optional; null when only the LID is known." example:"6281234567890"`
	Name         *string `json:"name,omitempty" doc:"The contact's display (push) name, when known. Optional; null when unknown." example:"Alice"`
	BusinessName *string `json:"businessName,omitempty" doc:"The verified business name, for WhatsApp Business accounts. Optional; null for non-business accounts." example:"Acme Corp"`
	// Source is where this session encountered the person: "dm" or "group".
	Source string `json:"source" enum:"dm,group" doc:"Where this session encountered the contact. **dm** = a direct chat with them exists; **group** = they were seen in a group this session is in." example:"dm"`
}

// Group mirrors the whatsapp_groups table.
type Group struct {
	ID               uint64  `json:"id" doc:"The group row id (internal numeric key)." example:"512"`
	GroupJID         string  `json:"groupJid" doc:"The group's WhatsApp JID — its stable identifier." example:"120363021234567890@g.us"`
	Subject          *string `json:"subject,omitempty" doc:"The group's name (its subject). Optional; null when unknown." example:"Project Team"`
	Description      *string `json:"description,omitempty" doc:"The group's description text. Optional; null when unset." example:"Coordination for Q3 launch"`
	OwnerJID         *string `json:"ownerJid,omitempty" doc:"JID of the group's owner/creator. Optional; null when unknown." example:"6281234567890@s.whatsapp.net"`
	ParticipantCount *int    `json:"participantCount,omitempty" doc:"Number of members in the group. Optional; null when not yet known." example:"42"`
	IsAnnounce       *bool   `json:"isAnnounce,omitempty" doc:"If true, only admins can post (announcement group). Optional; null when unknown." example:"false"`
	IsLocked         *bool   `json:"isLocked,omitempty" doc:"If true, only admins can edit the group's info (name, description, icon). Optional; null when unknown." example:"false"`
	CreatedAtWA      *int64  `json:"createdAtWa,omitempty" doc:"When the group was created on WhatsApp, in epoch milliseconds (UTC). Optional; null when unknown." example:"1700000000000"`
	FirstSeenAt      int64   `json:"firstSeenAt" doc:"When this group was first observed by the gateway, in epoch milliseconds (UTC)." example:"1719662400000"`
	UpdatedAt        int64   `json:"updatedAt" doc:"When this group's record was last updated, in epoch milliseconds (UTC)." example:"1719662400000"`
}

// GroupMember mirrors the whatsapp_group_members pivot. group_nickname is the
// per-group display name and lives here (not on Identity).
// GroupMember is one row of the identity↔group pivot: a person's membership of a
// group, observed by a session, with their role and per-group `tag` (the second
// per-group identity WhatsApp shows beside the global push name).
type GroupMember struct {
	ID          uint64    `json:"id" doc:"The membership pivot-row id (internal numeric key)." example:"7781"`
	SessionID   string    `json:"sessionId" doc:"Id of the session that observed this membership." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	GroupJID    string    `json:"groupJid" doc:"JID of the group this membership is in." example:"120363021234567890@g.us"`
	LID         string    `json:"lid" doc:"The member's LID (canonical, the identity key)." example:"205227043110953@lid"`
	Tag         *string   `json:"tag,omitempty" doc:"The per-group member tag WhatsApp shows beside the name — a second, group-specific identity (often the obfuscated phone for anonymous members), distinct from the global push name. Optional; null when absent." example:"~Alice"`
	Role        GroupRole `json:"role" enum:"member,admin,superadmin" doc:"The member's role in the group. **member** = ordinary participant; **admin** = group admin (can manage members and settings); **superadmin** = the group creator/owner." example:"member"`
	FirstSeenAt int64     `json:"firstSeenAt" doc:"When this membership was first observed, in epoch milliseconds (UTC)." example:"1719662400000"`
	LastSeenAt  int64     `json:"lastSeenAt" doc:"When this member was last seen in the group, in epoch milliseconds (UTC)." example:"1719662400000"`
}

// Chat mirrors the chats table.
type Chat struct {
	ID            uint64   `json:"id" doc:"The chat row id (internal numeric key)." example:"3344"`
	SessionID     string   `json:"sessionId" doc:"Id of the session this chat belongs to." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	ChatJID       string   `json:"jid" doc:"The chat's WhatsApp JID — its stable identifier." example:"6281234567890@s.whatsapp.net"` // REST/OpenAPI contract field is `jid` (matches the frontend Chat type + SSR reads)
	Aliases       []string `json:"aliases,omitempty" doc:"Equivalent JIDs for this chat. For DMs this can include both the canonical LID and linked phone JID, allowing clients to merge rows observed through either address." example:"205227043110953@lid"`
	Type          ChatType `json:"type" enum:"dm,group,newsletter,broadcast,status" doc:"Kind of chat. **dm** = one-to-one direct message; **group** = a group chat; **newsletter** = a WhatsApp channel; **broadcast** = a broadcast list; **status** = the status (stories) feed." example:"dm"`
	Name          *string  `json:"name,omitempty" doc:"The chat's display name (contact name for a DM, subject for a group). Optional; null when unknown." example:"Alice"`
	LastMessageAt *int64   `json:"lastMessageAt,omitempty" doc:"When the most recent message in this chat arrived, in epoch milliseconds (UTC). Optional; null if the chat has no messages." example:"1719662400000"`
	UnreadCount   int      `json:"unreadCount" doc:"Number of unread messages in this chat." example:"3"`
	Archived      bool     `json:"archived" doc:"Whether the chat is archived." example:"false"`
	Pinned        bool     `json:"pinned" doc:"Whether the chat is pinned to the top of the list." example:"false"`
	MutedUntil    *int64   `json:"mutedUntil,omitempty" doc:"Muted until this time, in epoch milliseconds (UTC). Optional; null/absent when the chat is not muted." example:"1751198400000"`
}

// MediaMeta is the typed shape of messages.media_meta JSON. Media is never
// downloaded in v1 — this is metadata only.
type MediaMeta struct {
	Mimetype string `json:"mimetype,omitempty" doc:"Media MIME type, e.g. image/jpeg. Optional." example:"image/jpeg"`
	Size     int64  `json:"size,omitempty" doc:"Media size in bytes. Optional." example:"204800"`
	Filename string `json:"filename,omitempty" doc:"Original filename, for document messages. Optional." example:"invoice.pdf"`
}

// Message mirrors the messages table.
type Message struct {
	ID          string  `json:"id" doc:"The stored message id (the WhatsApp message id, stable across the gateway)." example:"3EB0C431C26A1916E07A"`
	SessionID   string  `json:"sessionId" doc:"Id of the session this message belongs to." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	WAMessageID string  `json:"waMessageId" doc:"The raw WhatsApp message id as assigned by WhatsApp." example:"3EB0C431C26A1916E07A"`
	ChatJID     string  `json:"chatJid" doc:"JID of the chat this message belongs to." example:"6281234567890@s.whatsapp.net"`
	SenderLID   *string `json:"senderLid,omitempty" doc:"LID of whoever sent the message. In group chats the sender is identified by LID (senderJid is often absent). Optional; null when not applicable." example:"205227043110953@lid"`
	SenderJID   *string `json:"senderJid,omitempty" doc:"JID of whoever sent the message. Optional; null in group chats where only the LID is known." example:"6281234567890@s.whatsapp.net"`
	// SenderName is the resolved display name of the sender (from
	// whatsapp_identities, keyed by sender LID). Read-only: populated by the
	// message read queries via a join, never a stored column. Mainly useful for
	// group chats, where the sender differs per message and senderJid is absent.
	SenderName      *string          `json:"senderName,omitempty" doc:"Resolved display name of the sender, from whatsapp_identities (keyed by sender LID). Read-only: populated by a join at read time, never stored. Especially useful in group chats to label each message's author. Optional; null when unresolved." example:"Alice"`
	FromMe          bool             `json:"fromMe" doc:"True if this session sent the message; false if it was received." example:"false"`
	Direction       MessageDirection `json:"direction" enum:"in,out" doc:"Message direction. **in** = received by this session; **out** = sent by this session." example:"in"`
	Type            string           `json:"type" doc:"The message kind. Common values: text, image, video, audio, document, sticker, location, contact, poll, reaction, system." example:"text"`
	Body            *string          `json:"body,omitempty" doc:"The text content or media caption, when the message has one. Optional; null for messages with no text." example:"Hello there!"`
	QuotedMessageID *string          `json:"quotedMessageId,omitempty" doc:"Id of the message this one quotes/replies to. Optional; null when not a reply." example:"3EB0C431C26A1916E001"`
	Mentions        json.RawMessage  `json:"mentions,omitempty" doc:"JIDs @-mentioned in this message (group mentions), as stored — typically LIDs. JSON array. Optional; absent when there are no mentions." example:"[\"205227043110953@lid\"]"`
	// MentionNames resolves the @-mentions in Body to display names, keyed by the
	// mention's user-part (the token after "@" in the body, e.g. "205227043110953")
	// -> name. Read-only: populated by the message read queries from
	// whatsapp_identities, never stored. Only mentions resolvable to a known
	// identity appear; lets a client render "@<name>" instead of the raw number.
	MentionNames map[string]string `json:"mentionNames,omitempty" doc:"Resolved display names for the @-mentions, keyed by the mention's user-part — the token after '@' as it appears in 'body' (e.g. '205227043110953') → name. Read-only: populated from whatsapp_identities at read time, never stored. Only mentions resolvable to a known identity are included; lets a client render '@<name>' instead of the raw number. Optional." example:"{\"205227043110953\":\"Alice\"}"`
	HasMedia     bool              `json:"hasMedia" doc:"True if the message carries media (image/video/audio/document/sticker)." example:"false"`
	MediaMeta    *MediaMeta        `json:"media,omitempty" doc:"Media metadata (mimetype, size, filename) when the message has media. Metadata only — media is not downloaded in this build, so this is null even when hasMedia is true." `
	Status       *MessageStatus    `json:"status,omitempty" enum:"pending,sent,delivered,read,played,failed" doc:"Delivery state, mainly meaningful for outgoing messages. Progression: **pending** (queued, not yet confirmed) → **sent** (handed to WhatsApp) → **delivered** (reached the recipient's device) → **read** (opened) → **played** (voice/video note played). **failed** = could not be sent. Optional; null for inbound messages with no tracked status." example:"delivered"`
	AckLevel     *int              `json:"ackLevel,omitempty" doc:"Raw WhatsApp acknowledgement level underlying status (0=pending … up to played). Optional; null when not tracked." example:"3"`
	Error        *string           `json:"error,omitempty" doc:"Failure reason when status is failed. Optional; null otherwise." example:"recipient not on WhatsApp"`
	Edited       bool              `json:"edited" doc:"True if the message was edited after being sent." example:"false"`
	Deleted      bool              `json:"deleted" doc:"True if the message was deleted/revoked." example:"false"`
	Timestamp    int64             `json:"timestamp" doc:"When the message was sent or received, in epoch milliseconds (UTC)." example:"1719662400000"`
	RawJSON      json.RawMessage   `json:"-"` // normalized event payload; never re-exposed
	CreatedAt    int64             `json:"createdAt" doc:"When the message row was stored, in epoch milliseconds (UTC)." example:"1719662400000"`
}

// BackfillSnapshot is the supported data pulled from a live WhatsApp session by
// an admin-triggered background backfill. WhatsApp chat-message history is
// event-driven through HistorySync; this snapshot covers data with direct APIs.
type BackfillSnapshot struct {
	Contacts []BackfillContact `json:"contacts,omitempty" doc:"Contacts pulled from the live session via direct WhatsApp APIs. Optional; absent when none."`
	Groups   []BackfillGroup   `json:"groups,omitempty" doc:"Groups (with members) pulled from the live session via direct WhatsApp APIs. Optional; absent when none."`
}

type BackfillContact struct {
	// LID is the canonical (non-AD) LID this contact resolves to — the identity
	// table's key. Empty when the contact has no known LID mapping.
	LID          string `json:"lid" doc:"The canonical (non-AD) LID this contact resolves to — the identity table's key. Empty when the contact has no known LID mapping." example:"205227043110953@lid"`
	PhoneJID     string `json:"phoneJid,omitempty" doc:"The contact's phone-based WhatsApp JID. Optional; empty when unknown." example:"6281234567890@s.whatsapp.net"`
	PhoneNumber  string `json:"phoneNumber,omitempty" doc:"The contact's phone number in plain digits. Optional; empty when unknown." example:"6281234567890"`
	Name         string `json:"name,omitempty" doc:"The contact's display (push) name. Optional; empty when unknown." example:"Alice"`
	BusinessName string `json:"businessName,omitempty" doc:"The verified business name, for business accounts. Optional; empty otherwise." example:"Acme Corp"`
}

type BackfillGroup struct {
	GroupJID     string           `json:"groupJid" doc:"The group's WhatsApp JID." example:"120363021234567890@g.us"`
	Subject      string           `json:"subject,omitempty" doc:"The group's name (subject). Optional; empty when unknown." example:"Project Team"`
	Description  string           `json:"description,omitempty" doc:"The group's description text. Optional; empty when unset." example:"Coordination for Q3 launch"`
	OwnerJID     string           `json:"ownerJid,omitempty" doc:"JID of the group's owner/creator. Optional; empty when unknown." example:"6281234567890@s.whatsapp.net"`
	Participants int              `json:"participants,omitempty" doc:"Number of members in the group. Optional; 0/absent when unknown." example:"42"`
	IsAnnounce   bool             `json:"isAnnounce" doc:"If true, only admins can post (announcement group)." example:"false"`
	IsLocked     bool             `json:"isLocked" doc:"If true, only admins can edit the group's info (name, description, icon)." example:"false"`
	CreatedAtWA  int64            `json:"createdAtWa,omitempty" doc:"When the group was created on WhatsApp, in epoch milliseconds (UTC). Optional; 0/absent when unknown." example:"1700000000000"`
	Members      []BackfillMember `json:"members,omitempty" doc:"The group's members captured in this backfill. Optional; absent when none."`
}

type BackfillMember struct {
	// LID is the canonical (non-AD) LID of the participant — both the group-member
	// row key and the identity row this member contributes.
	LID         string `json:"lid" doc:"The canonical (non-AD) LID of the participant — both the group-member row key and the identity row this member contributes." example:"205227043110953@lid"`
	JID         string `json:"jid,omitempty" doc:"The participant's phone-based WhatsApp JID. Optional; empty when unknown." example:"6281234567890@s.whatsapp.net"`
	PhoneNumber string `json:"phoneNumber,omitempty" doc:"The participant's phone number in plain digits. Optional; empty when unknown." example:"6281234567890"`
	// Tag is the per-group member tag WhatsApp shows beside the name (often the
	// obfuscated phone for anonymous members). Stored on the pivot as-is — it is a
	// per-group identity, distinct from the global push name.
	Tag string `json:"tag,omitempty" doc:"The per-group member tag WhatsApp shows beside the name (often the obfuscated phone for anonymous members). Stored on the pivot as-is — a per-group identity, distinct from the global push name. Optional; empty when absent." example:"~Alice"`
	// Name is a real display name resolved for this participant (contact store /
	// push name), when known — used to seed the identity row.
	Name string    `json:"name,omitempty" doc:"A real display name resolved for this participant (contact store / push name), when known. Used to seed the identity row. Optional; empty when unknown." example:"Alice"`
	Role GroupRole `json:"role" enum:"member,admin,superadmin" doc:"The participant's role in the group. **member** = ordinary participant; **admin** = group admin; **superadmin** = the group creator/owner." example:"member"`
}

// BackfillJob describes an in-memory background admin backfill job.
type BackfillJob struct {
	ID             string `json:"id" doc:"The backfill job id." example:"01J9..."`
	SessionID      string `json:"sessionId" doc:"Id of the session being backfilled." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	OrganizationID string `json:"organizationId" doc:"Id of the organization that owns the session." example:"org_01J9ABC..."`
	Status         string `json:"status" enum:"running,succeeded,failed" doc:"Current job state. **running** = in progress; **succeeded** = completed and data persisted; **failed** = aborted (see error). Progression: running → succeeded, or running → failed." example:"succeeded"`
	Contacts       int    `json:"contacts" doc:"Number of contacts persisted so far." example:"128"`
	Groups         int    `json:"groups" doc:"Number of groups persisted so far." example:"14"`
	GroupMembers   int    `json:"groupMembers" doc:"Number of group memberships persisted so far." example:"342"`
	Error          string `json:"error,omitempty" doc:"Failure message when status is failed. Optional; empty otherwise." example:"session not connected"`
	StartedAt      int64  `json:"startedAt" doc:"When the job started, in epoch milliseconds (UTC)." example:"1719662400000"`
	FinishedAt     *int64 `json:"finishedAt,omitempty" doc:"When the job finished, in epoch milliseconds (UTC). Optional; null while still running." example:"1719662460000"`
}

// BackfillImport mirrors the backfill_imports table — a durable record of a
// user-initiated WhatsApp backup (crypt15) import. It is both the dashboard's
// job-status surface and the source of truth for the once-per-24h-per-session
// import quota (super_admins bypass). Distinct from BackfillJob, which is the
// in-memory admin live-data backfill.
type BackfillImport struct {
	ID                string  `json:"id" doc:"The import job id." example:"01J9..."`
	SessionID         string  `json:"sessionId" doc:"Id of the session being backfilled." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	OrganizationID    string  `json:"organizationId" doc:"Id of the organization that owns the session." example:"org_01J9ABC..."`
	Source            string  `json:"source" doc:"The backup source format. Currently always crypt15 (an encrypted WhatsApp chat backup)." example:"crypt15"`
	Status            string  `json:"status" enum:"running,succeeded,failed" doc:"Current job state. **running** = import in progress; **succeeded** = completed and rows upserted; **failed** = aborted (see error). Progression: running → succeeded, or running → failed." example:"succeeded"`
	Chats             int     `json:"chats" doc:"Number of chats upserted." example:"57"`
	Messages          int     `json:"messages" doc:"Number of messages upserted." example:"12043"`
	Identities        int     `json:"identities" doc:"Number of identities upserted." example:"310"`
	Groups            int     `json:"groups" doc:"Number of groups upserted." example:"14"`
	GroupMembers      int     `json:"groupMembers" doc:"Number of group memberships upserted." example:"342"`
	SchemaFingerprint *string `json:"schemaFingerprint,omitempty" doc:"Detected backup schema fingerprint (WhatsApp build id + capability hash), recorded for diagnosing format drift. Optional; null when not detected." example:"2.24.x:ab12cd"`
	Error             string  `json:"error,omitempty" doc:"Failure message when status is failed. Optional; empty otherwise." example:"unsupported backup schema"`
	CreatedAt         int64   `json:"createdAt" doc:"When the import started, in epoch milliseconds (UTC)." example:"1719662400000"`
	FinishedAt        *int64  `json:"finishedAt,omitempty" doc:"When the import finished, in epoch milliseconds (UTC). Optional; null while still running." example:"1719662460000"`
}

// Poll mirrors the polls table: the option list of a poll-creation message,
// kept so incoming votes (which carry only SHA-256 hashes of the chosen options)
// can be resolved back to readable option text.
type Poll struct {
	ID              uint64   `json:"id" doc:"The poll row id (internal numeric key)." example:"3401"`
	SessionID       string   `json:"sessionId" doc:"Id of the session that observed the poll." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	PollMessageID   string   `json:"pollMessageId" doc:"Id of the poll-creation message." example:"3EB0C431C26A1916E07A"`
	ChatJID         string   `json:"chatJid" doc:"JID of the chat the poll was posted in."`
	Name            string   `json:"name" doc:"The poll question."`
	Options         []string `json:"options" doc:"The poll answer options, in creation order."`
	SelectableCount int      `json:"selectableCount" doc:"How many options a voter may select (1 = single choice)." example:"1"`
	EndTime         int64    `json:"endTime,omitempty" doc:"Poll closing time as epoch milliseconds, when WhatsApp provided one." example:"1719662400000"`
	HideVotes       bool     `json:"hideVotes,omitempty" doc:"True when WhatsApp hides participant names in the poll vote list." example:"true"`
	CreatedAt       int64    `json:"createdAt" doc:"When the poll was first recorded, epoch milliseconds."`
	UpdatedAt       int64    `json:"updatedAt" doc:"When the poll row was last updated, epoch milliseconds."`
}

// PollVote mirrors the poll_votes table.
type PollVote struct {
	ID              uint64          `json:"id" doc:"The poll-vote row id (internal numeric key)." example:"9012"`
	SessionID       string          `json:"sessionId" doc:"Id of the session that recorded the vote." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	PollMessageID   string          `json:"pollMessageId" doc:"Id of the poll message this vote is for." example:"3EB0C431C26A1916E07A"`
	VoterLID        string          `json:"voterLid" doc:"LID of the voter." example:"205227043110953@lid"`
	SelectedOptions json.RawMessage `json:"selectedOptions" doc:"The option(s) the voter selected, as a JSON array of option identifiers." example:"[\"Yes\"]"`
	Timestamp       int64           `json:"timestamp" doc:"When the vote was cast, in epoch milliseconds (UTC)." example:"1719662400000"`
	RawJSON         json.RawMessage `json:"-"` // normalized event payload; never re-exposed
}

type PollRecapCandidate struct {
	SessionID       string   `json:"sessionId"`
	OrganizationID  string   `json:"organizationId"`
	PollMessageID   string   `json:"pollMessageId"`
	ChatJID         string   `json:"chatJid"`
	Name            string   `json:"name"`
	Options         []string `json:"options"`
	SelectableCount int      `json:"selectableCount"`
	EndTime         int64    `json:"endTime"`
	HideVotes       bool     `json:"hideVotes"`
}

type PollRecapOption struct {
	Option string `json:"option"`
	Count  int    `json:"count"`
}

type PollRecapVoter struct {
	VoterID         string   `json:"voterId" doc:"Stored voter key from poll_votes: the sender LID, or the sender phone JID when no LID was available." example:"205227043110953@lid"`
	DisplayName     string   `json:"displayName" doc:"Display name resolved from whatsapp_identities at read time. Empty when the voter has no known identity name." example:"Sam Lee"`
	SelectedOptions []string `json:"selectedOptions" doc:"Options selected by this voter's latest vote." example:"[\"Yes\",\"Maybe\"]"`
}

type PollRecapPayload struct {
	PollMessageID   string            `json:"pollMessageId" doc:"Id of the poll-creation message."`
	ChatJID         string            `json:"chatJid" doc:"JID of the chat the poll belongs to."`
	Name            string            `json:"name" doc:"The poll question."`
	Options         []PollRecapOption `json:"options" doc:"Vote totals by option."`
	SelectableCount int               `json:"selectableCount" doc:"How many options a voter could select."`
	EndTime         int64             `json:"endTime" doc:"Poll closing time as epoch milliseconds."`
	HideVotes       bool              `json:"hideVotes" doc:"True when participant names were hidden in the vote list."`
	TotalVotes      int               `json:"totalVotes" doc:"Number of latest voter records counted."`
	Voters          []PollRecapVoter  `json:"voters,omitempty" doc:"Per-voter latest selections, included only when hideVotes is false."`
}

// OutboxEntry mirrors the outbox table (async send queue).
type OutboxEntry struct {
	ID             string          `json:"id" doc:"The outbox entry id (a ULID). Identifies one queued outbound send." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	OrganizationID string          `json:"organizationId" doc:"Id of the organization that owns the send." example:"org_01J9ABC..."`
	SessionID      string          `json:"sessionId" doc:"Id of the session the message is sent from." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	IdempotencyKey *string         `json:"idempotencyKey,omitempty" doc:"Caller-supplied idempotency key: a repeat send with the same key returns the original result instead of sending again. Optional; null when the caller did not supply one." example:"order-4821-confirm"`
	Payload        json.RawMessage `json:"payload" doc:"The original send request payload (a SendRequest), stored verbatim as JSON."`
	Status         OutboxStatus    `json:"status" enum:"queued,sending,sent,failed" doc:"State of the queued send. **queued** = accepted, awaiting a worker; **sending** = a worker is delivering it to WhatsApp; **sent** = handed off successfully (see waMessageId); **failed** = all attempts failed (see error). Progression: queued → sending → sent, or queued → sending → failed." example:"sent"`
	Attempts       int             `json:"attempts" doc:"How many delivery attempts have been made." example:"1"`
	WAMessageID    *string         `json:"waMessageId,omitempty" doc:"The WhatsApp message id assigned once the send succeeded. Optional; null until status is sent." example:"3EB0C431C26A1916E07A"`
	Error          *string         `json:"error,omitempty" doc:"Failure reason when status is failed. Optional; null otherwise." example:"rate limited"`
	CreatedAt      int64           `json:"createdAt" doc:"When the entry was enqueued, in epoch milliseconds (UTC)." example:"1719662400000"`
	UpdatedAt      int64           `json:"updatedAt" doc:"When the entry was last updated, in epoch milliseconds (UTC)." example:"1719662400000"`
}

// EventLogEntry mirrors the event_log table. ID is the monotonic DB cursor used
// for stream resume (?since=); EventID is the ULID exposed to clients.
type EventLogEntry struct {
	ID             uint64          `json:"-"` // monotonic cursor (internal; used for stream resume via ?since=, not serialized)
	EventID        string          `json:"id" doc:"The event's id (a ULID). Stable identifier exposed to clients; webhooks echo it so receivers can drop duplicate redeliveries." example:"evt_01J9..."`
	OrganizationID string          `json:"organization" doc:"Id of the organization that owns the session the event came from." example:"org_01J9ABC..."`
	SessionID      string          `json:"session" doc:"Id of the session the event came from." example:"01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Type           string          `json:"event" doc:"The event type, e.g. message, session.status, group.update, poll.vote." example:"message"`
	Payload        json.RawMessage `json:"payload" doc:"The event's body as JSON; its shape depends on the event type."`
	CreatedAt      int64           `json:"createdAt" doc:"When the event was recorded, in epoch milliseconds (UTC)." example:"1719662400000"`
}
