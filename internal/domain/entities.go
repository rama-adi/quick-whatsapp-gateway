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

// Tenant mirrors the tenants table — a thin app-side mirror of an Authula user.
type Tenant struct {
	ID          string  `json:"id"` // = Authula user id
	Email       string  `json:"email"`
	DisplayName *string `json:"displayName,omitempty"`
	CreatedAt   int64   `json:"createdAt"`
	UpdatedAt   int64   `json:"updatedAt"`
}

// WASession mirrors the wa_sessions table (an attached WhatsApp number).
type WASession struct {
	ID              string        `json:"id"` // app session id (ULID)
	TenantID        string        `json:"tenantId"`
	Label           *string       `json:"label,omitempty"`
	Status          SessionStatus `json:"status"`
	WAJID           *string       `json:"waJid,omitempty"` // phone JID once paired
	WALID           *string       `json:"waLid,omitempty"` // linked-device id
	PhoneNumber     *string       `json:"phoneNumber,omitempty"`
	IsAdminSession  bool          `json:"isAdminSession"` // the WHATSAPP_ADMIN_NUMBER session
	AutoRead        bool          `json:"autoRead"`       // mark inbound read before acting
	PresenceTyping  bool          `json:"presenceTyping"`
	RatePerMin      int           `json:"ratePerMin"`
	RatePerHour     int           `json:"ratePerHour"`
	LastConnectedAt *int64        `json:"lastConnectedAt,omitempty"`
	CreatedAt       int64         `json:"createdAt"`
	UpdatedAt       int64         `json:"updatedAt"`
}

// Permissions is the typed shape of api_keys.permissions JSON.
type Permissions struct {
	Read   bool `json:"read"`
	Send   bool `json:"send"`
	Manage bool `json:"manage"`
	Events bool `json:"events"`
}

// APIKey mirrors the api_keys table. Account-global: belongs to a tenant, valid
// across all that tenant's sessions; the session is targeted by route, not key.
// KeyHash is never serialized (argon2id of the full key); the full key is shown
// once at creation only (see CreateAPIKeyResult).
type APIKey struct {
	ID          string      `json:"id"`
	TenantID    string      `json:"tenantId"`
	Name        string      `json:"name"`
	KeyPrefix   string      `json:"keyPrefix"` // shown in UI, e.g. "wak_ab12"
	KeyHash     string      `json:"-"`         // argon2id of full key; never exposed
	Scope       APIKeyScope `json:"scope"`
	Permissions Permissions `json:"permissions"`
	LastUsedAt  *int64      `json:"lastUsedAt,omitempty"`
	ExpiresAt   *int64      `json:"expiresAt,omitempty"`
	RevokedAt   *int64      `json:"revokedAt,omitempty"`
	CreatedAt   int64       `json:"createdAt"`
}

// RetryPolicy is the typed shape of webhooks.retry_policy JSON.
type RetryPolicy struct {
	Policy       string `json:"policy"`       // e.g. "exponential"
	DelaySeconds int    `json:"delaySeconds"` // base delay
	Attempts     int    `json:"attempts"`     // max attempts before "dead"
}

// Webhook mirrors the webhooks table. HMACSecret is stored AES-GCM encrypted at
// rest and never serialized.
type Webhook struct {
	ID            string            `json:"id"`
	TenantID      string            `json:"tenantId"`
	SessionID     *string           `json:"sessionId,omitempty"` // null = all tenant sessions
	URL           string            `json:"url"`
	Events        []string          `json:"events"` // ["message","poll.vote"] or ["*"]
	HMACSecret    []byte            `json:"-"`      // AES-GCM encrypted at rest
	CustomHeaders map[string]string `json:"customHeaders,omitempty"`
	RetryPolicy   RetryPolicy       `json:"retryPolicy"`
	Active        bool              `json:"active"`
	CreatedAt     int64             `json:"createdAt"`
	UpdatedAt     int64             `json:"updatedAt"`
}

// WebhookDelivery mirrors the webhook_deliveries table.
type WebhookDelivery struct {
	ID           uint64                `json:"id"`
	WebhookID    string                `json:"webhookId"`
	EventID      string                `json:"eventId"`
	Status       WebhookDeliveryStatus `json:"status"`
	Attempts     int                   `json:"attempts"`
	ResponseCode *int                  `json:"responseCode,omitempty"`
	NextRetryAt  *int64                `json:"nextRetryAt,omitempty"`
	LastError    *string               `json:"lastError,omitempty"`
	CreatedAt    int64                 `json:"createdAt"`
}

// Identity mirrors the whatsapp_identities table — global LID->phone/name
// resolution. Push name lives here; nickname is group-specific (see GroupMember).
type Identity struct {
	ID           uint64  `json:"id"`
	LID          string  `json:"lid"`
	PhoneNumber  *string `json:"phoneNumber,omitempty"`
	PhoneJID     *string `json:"phoneJid,omitempty"`
	Name         *string `json:"name,omitempty"` // push name (preferred display)
	BusinessName *string `json:"businessName,omitempty"`
	FirstSeenAt  int64   `json:"firstSeenAt"`
	UpdatedAt    int64   `json:"updatedAt"`
}

// Contact mirrors the whatsapp_contacts table — a per-account "found user"
// record powering the Contacts feature (where this session encountered them).
type Contact struct {
	ID            uint64 `json:"id"`
	SessionID     string `json:"sessionId"`
	LID           string `json:"lid"`
	SeenInDM      bool   `json:"seenInDm"`
	DMFirstSeenAt *int64 `json:"dmFirstSeenAt,omitempty"`
	DMLastSeenAt  *int64 `json:"dmLastSeenAt,omitempty"`
	MessageCount  int64  `json:"messageCount"`
	FirstSeenAt   int64  `json:"firstSeenAt"`
	LastSeenAt    int64  `json:"lastSeenAt"`
}

// Group mirrors the whatsapp_groups table.
type Group struct {
	ID               uint64  `json:"id"`
	GroupJID         string  `json:"groupJid"`
	Subject          *string `json:"subject,omitempty"`
	Description      *string `json:"description,omitempty"`
	OwnerJID         *string `json:"ownerJid,omitempty"`
	ParticipantCount *int    `json:"participantCount,omitempty"`
	IsAnnounce       *bool   `json:"isAnnounce,omitempty"`
	IsLocked         *bool   `json:"isLocked,omitempty"`
	CreatedAtWA      *int64  `json:"createdAtWa,omitempty"`
	FirstSeenAt      int64   `json:"firstSeenAt"`
	UpdatedAt        int64   `json:"updatedAt"`
}

// GroupMember mirrors the whatsapp_group_members pivot. group_nickname is the
// per-group display name and lives here (not on Identity).
type GroupMember struct {
	ID            uint64    `json:"id"`
	SessionID     string    `json:"sessionId"`
	GroupJID      string    `json:"groupJid"`
	LID           string    `json:"lid"`
	GroupNickname *string   `json:"groupNickname,omitempty"`
	Role          GroupRole `json:"role"`
	FirstSeenAt   int64     `json:"firstSeenAt"`
	LastSeenAt    int64     `json:"lastSeenAt"`
}

// Chat mirrors the chats table.
type Chat struct {
	ID            uint64   `json:"id"`
	SessionID     string   `json:"sessionId"`
	ChatJID       string   `json:"chatJid"`
	Type          ChatType `json:"type"`
	Name          *string  `json:"name,omitempty"`
	LastMessageAt *int64   `json:"lastMessageAt,omitempty"`
	UnreadCount   int      `json:"unreadCount"`
	Archived      bool     `json:"archived"`
	Pinned        bool     `json:"pinned"`
	MutedUntil    *int64   `json:"mutedUntil,omitempty"`
}

// MediaMeta is the typed shape of messages.media_meta JSON. Media is never
// downloaded in v1 — this is metadata only.
type MediaMeta struct {
	Mimetype string `json:"mimetype,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// Message mirrors the messages table.
type Message struct {
	ID              uint64           `json:"id"`
	SessionID       string           `json:"sessionId"`
	WAMessageID     string           `json:"waMessageId"`
	ChatJID         string           `json:"chatJid"`
	SenderLID       *string          `json:"senderLid,omitempty"`
	SenderJID       *string          `json:"senderJid,omitempty"`
	FromMe          bool             `json:"fromMe"`
	Direction       MessageDirection `json:"direction"`
	Type            string           `json:"type"` // text,poll,location,contact,reaction,system,image,...
	Body            *string          `json:"body,omitempty"`
	QuotedMessageID *string          `json:"quotedMessageId,omitempty"`
	Mentions        json.RawMessage  `json:"mentions,omitempty"`
	HasMedia        bool             `json:"hasMedia"`
	MediaMeta       *MediaMeta       `json:"media,omitempty"` // null in v1 (metadata-only)
	Status          *MessageStatus   `json:"status,omitempty"`
	AckLevel        *int             `json:"ackLevel,omitempty"`
	Error           *string          `json:"error,omitempty"`
	Edited          bool             `json:"edited"`
	Deleted         bool             `json:"deleted"`
	Timestamp       int64            `json:"timestamp"`
	RawJSON         json.RawMessage  `json:"-"` // normalized event payload; not re-exposed
	CreatedAt       int64            `json:"createdAt"`
}

// PollVote mirrors the poll_votes table.
type PollVote struct {
	ID              uint64          `json:"id"`
	SessionID       string          `json:"sessionId"`
	PollMessageID   string          `json:"pollMessageId"`
	VoterLID        string          `json:"voterLid"`
	SelectedOptions json.RawMessage `json:"selectedOptions"`
	Timestamp       int64           `json:"timestamp"`
	RawJSON         json.RawMessage `json:"-"`
}

// OutboxEntry mirrors the outbox table (async send queue).
type OutboxEntry struct {
	ID             string          `json:"id"` // ULID
	TenantID       string          `json:"tenantId"`
	SessionID      string          `json:"sessionId"`
	IdempotencyKey *string         `json:"idempotencyKey,omitempty"`
	Payload        json.RawMessage `json:"payload"`
	Status         OutboxStatus    `json:"status"`
	Attempts       int             `json:"attempts"`
	WAMessageID    *string         `json:"waMessageId,omitempty"`
	Error          *string         `json:"error,omitempty"`
	CreatedAt      int64           `json:"createdAt"`
	UpdatedAt      int64           `json:"updatedAt"`
}

// EventLogEntry mirrors the event_log table. ID is the monotonic DB cursor used
// for stream resume (?since=); EventID is the ULID exposed to clients.
type EventLogEntry struct {
	ID        uint64          `json:"-"` // monotonic cursor (internal)
	EventID   string          `json:"id"`
	TenantID  string          `json:"tenant"`
	SessionID string          `json:"session"`
	Type      string          `json:"event"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt int64           `json:"createdAt"`
}
