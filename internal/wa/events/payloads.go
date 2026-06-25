package events

import "github.com/ramaadi/quick-whatsapp-gateway/internal/domain"

// This file defines (a) the wire payload structs that ride inside a domain.Event
// (camelCase JSON, §9/§11 conventions, NEVER containing a raw protobuf) and
// (b) PersistResult, the structured handoff the inbound pipeline consumes after
// normalization (§7 stages: capture / persist / fan-out).

// --- Wire payloads (the "payload" of domain.Event) ---

// SessionStatusPayload — event "session.status".
type SessionStatusPayload struct {
	Status string `json:"status"` // a domain.SessionStatus value (e.g. "working")
}

// AuthQRPayload — event "auth.qr". The QR string the UI renders.
type AuthQRPayload struct {
	Code string `json:"code"`
}

// AuthCodePayload — event "auth.code". Emitted on PairSuccess; the linked JID/LID.
type AuthCodePayload struct {
	JID          string `json:"jid,omitempty"`
	LID          string `json:"lid,omitempty"`
	BusinessName string `json:"businessName,omitempty"`
	Platform     string `json:"platform,omitempty"`
}

// MessagePayload — events "message" / "message.from_me" / "message.reaction" /
// "message.edited" / "message.revoked" / "poll.vote". A single flat shape
// discriminated by the surrounding Event.Type, mirroring the messages table.
// Media is always null in v1 (HasMedia + Media metadata only).
type MessagePayload struct {
	WAMessageID     string     `json:"waMessageId"`
	ChatJID         string     `json:"chatJid"`
	SenderJID       string     `json:"senderJid,omitempty"`
	SenderLID       string     `json:"senderLid,omitempty"`
	FromMe          bool       `json:"fromMe"`
	Type            string     `json:"type"` // text,location,contact,poll,reaction,edit,revoke,media,...
	Body            string     `json:"body,omitempty"`
	QuotedMessageID string     `json:"quotedMessageId,omitempty"`
	Mentions        []string   `json:"mentions,omitempty"`
	HasMedia        bool       `json:"hasMedia"`
	Media           *MediaMeta `json:"media"` // ALWAYS null in v1 (metadata-only): see HasMedia + MediaInfo on PersistResult
	Timestamp       int64      `json:"timestamp"`
	PushName        string     `json:"pushName,omitempty"`
	// Reaction / edit / revoke / poll specifics (set only for the relevant Type):
	Reaction       string        `json:"reaction,omitempty"`       // emoji for message.reaction ("" = removed)
	TargetID       string        `json:"targetId,omitempty"`       // edited/revoked/reacted target message id
	Location       *LocationData `json:"location,omitempty"`       // for type "location"
	Contact        *ContactData  `json:"contact,omitempty"`        // for type "contact"
	Poll           *PollData     `json:"poll,omitempty"`           // for type "poll"
	SelectedHashes []string      `json:"selectedHashes,omitempty"` // for poll.vote (encrypted option hashes)
}

// MediaMeta is the on-wire media descriptor. Per §9 the "media" field is always
// null in v1, so this is never actually serialized into MessagePayload.Media; the
// parsed values travel on PersistResult.MediaInfo for the persistence layer.
type MediaMeta struct {
	Mimetype string `json:"mimetype,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// LocationData is the wire shape for a location message.
type LocationData struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"name,omitempty"`
	Address   string  `json:"address,omitempty"`
}

// ContactData is the wire shape for a shared contact card (vCard kept verbatim).
type ContactData struct {
	DisplayName string `json:"displayName,omitempty"`
	VCard       string `json:"vcard,omitempty"`
}

// PollData is the wire shape for a poll-creation message.
type PollData struct {
	Name            string   `json:"name"`
	Options         []string `json:"options"`
	SelectableCount int      `json:"selectableCount"`
}

// MessageStatusPayload — event "message.status" (from receipts).
type MessageStatusPayload struct {
	ChatJID    string   `json:"chatJid"`
	SenderJID  string   `json:"senderJid,omitempty"`
	MessageIDs []string `json:"messageIds"`
	Status     string   `json:"status"` // a domain.MessageStatus value
	Timestamp  int64    `json:"timestamp"`
}

// PresencePayload — event "presence.update" (Presence + ChatPresence).
type PresencePayload struct {
	ChatJID     string `json:"chatJid,omitempty"`
	From        string `json:"from"`
	State       string `json:"state"`           // available|unavailable|composing|paused
	Media       string `json:"media,omitempty"` // ChatPresence media kind (text|audio)
	Unavailable bool   `json:"unavailable,omitempty"`
	LastSeen    int64  `json:"lastSeen,omitempty"`
}

// GroupPayload — events "group.update" / "group.participant".
type GroupPayload struct {
	GroupJID      string   `json:"groupJid"`
	Subject       string   `json:"subject,omitempty"`
	Description   string   `json:"description,omitempty"`
	Sender        string   `json:"sender,omitempty"`
	IsAnnounce    *bool    `json:"isAnnounce,omitempty"`
	IsLocked      *bool    `json:"isLocked,omitempty"`
	NewInviteLink string   `json:"newInviteLink,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	Join          []string `json:"join,omitempty"`
	Leave         []string `json:"leave,omitempty"`
	Promote       []string `json:"promote,omitempty"`
	Demote        []string `json:"demote,omitempty"`
	Timestamp     int64    `json:"timestamp,omitempty"`
}

// ChatUpdatePayload — event "chat.update" (currently profile-picture changes).
type ChatUpdatePayload struct {
	ChatJID   string `json:"chatJid"`
	Change    string `json:"change"` // e.g. "picture"
	PictureID string `json:"pictureId,omitempty"`
	Removed   bool   `json:"removed,omitempty"`
}

// ContactUpdatePayload — event "contact.update" (PushName / Contact actions).
type ContactUpdatePayload struct {
	JID       string `json:"jid"`
	PushName  string `json:"pushName,omitempty"`
	FullName  string `json:"fullName,omitempty"`
	FirstName string `json:"firstName,omitempty"`
}

// CallPayload — event "call.incoming".
type CallPayload struct {
	CallID    string `json:"callId"`
	From      string `json:"from"`
	Timestamp int64  `json:"timestamp"`
	IsGroup   bool   `json:"isGroup"`
	GroupJID  string `json:"groupJid,omitempty"`
}

// NewsletterPayload — event "newsletter.update".
type NewsletterPayload struct {
	JID    string `json:"jid"`
	Action string `json:"action"` // join|leave|mute
}

// --- PersistResult: the inbound-pipeline handoff ---

// PersistKind tags what downstream work a normalized event implies, so the
// inbound pipeline can dispatch without re-inspecting the event type.
type PersistKind int

const (
	PersistNone             PersistKind = iota // ephemeral (qr/presence/call/newsletter)
	PersistMessage                             // insert/upsert a messages row (+ chat, contacts)
	PersistMessageReaction                     // a reaction on an existing message
	PersistMessageEdit                         // an edit of an existing message
	PersistMessageRevoke                       // a revoke (delete-for-everyone)
	PersistPollVote                            // a poll vote (poll_votes row)
	PersistMessageStatus                       // a receipt -> messages.status/ack_level
	PersistSessionStatus                       // wa_sessions.status change
	PersistGroupUpdate                         // whatsapp_groups upsert
	PersistGroupParticipant                    // whatsapp_group_members change
	PersistContactUpdate                       // whatsapp_identities/contacts upsert
)

// PersistResult is the structured, protobuf-free view the inbound pipeline
// consumes after Normalize (§7). It carries everything the capture/persist
// stages need without re-parsing the raw event. For message-bearing events the
// fully parsed NormalizedMessage is attached.
type PersistResult struct {
	Kind PersistKind

	// Set for message-bearing kinds (PersistMessage / Reaction / Edit / Revoke /
	// PollVote). Nil otherwise.
	Message *NormalizedMessage

	// Common locator fields, set where meaningful regardless of kind.
	ChatJID string

	// PersistMessageStatus (receipts):
	MessageStatus domain.MessageStatus
	MessageIDs    []string

	// PersistSessionStatus:
	SessionStatus domain.SessionStatus

	// PersistContactUpdate:
	ContactJID  string
	ContactName string
	PushName    string
}

// NormalizedMessage is the fully-parsed, protobuf-free representation of an
// inbound *events.Message that the capture + persistence layers consume. It maps
// almost 1:1 onto the messages table (§5) plus the identity/contacts capture
// inputs (§7 stage 3). The Subtype distinguishes the in-band variants the
// catalog splits into separate events.
type NormalizedMessage struct {
	WAMessageID string
	ChatJID     string
	ChatClass   ChatClass // dm/group/newsletter/broadcast/status (drives chats.type + ignore)
	SenderJID   string
	SenderLID   string
	FromMe      bool
	PushName    string
	Timestamp   int64 // epoch-ms

	Subtype MessageSubtype // text/media/location/contact/poll/reaction/edit/revoke/poll_vote
	// MessageType is the persisted messages.type string (text,image,location,...).
	MessageType string

	Body            string   // text body / caption
	QuotedMessageID string   // reply target stanza id
	Mentions        []string // mentioned JID strings

	// Media (metadata only — NEVER downloaded in v1).
	HasMedia  bool
	MediaInfo *MediaMeta

	// Reaction / edit / revoke: the id of the message being acted on.
	TargetMessageID string
	Reaction        string // emoji for reactions ("" = removed)

	// Structured bodies for the typed sub-messages.
	Location *LocationData
	Contact  *ContactData
	Poll     *PollData

	// Poll vote (incoming PollUpdateMessage, still encrypted at normalize time):
	// the encrypted selected-option hashes; decryption happens later in the
	// pipeline via cli.DecryptPollVote.
	PollVoteTargetID string   // the poll-creation message id being voted on
	SelectedHashes   []string // encrypted option hashes
}

// MessageSubtype enumerates the in-band variants of an *events.Message.
type MessageSubtype int

const (
	SubtypeText     MessageSubtype = iota // Conversation / ExtendedTextMessage
	SubtypeMedia                          // image/video/audio/document/sticker (metadata only)
	SubtypeLocation                       // LocationMessage
	SubtypeContact                        // ContactMessage
	SubtypePoll                           // PollCreationMessage
	SubtypeReaction                       // ReactionMessage
	SubtypeEdit                           // ProtocolMessage{MESSAGE_EDIT} / IsEdit
	SubtypeRevoke                         // ProtocolMessage{REVOKE}
	SubtypePollVote                       // PollUpdateMessage (a vote)
	SubtypeUnknown                        // recognized as a message but no known body
)
