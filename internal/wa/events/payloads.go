package events

import (
	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// The wire payload structs (the "payload" of a domain.Event) are defined ONCE in
// internal/apitypes (the source of truth for the generated OpenAPI event catalog,
// D11) and aliased here so the inbound pipeline keeps using the familiar
// events.<Payload> names with no duplication. This file also defines PersistResult,
// the structured handoff the inbound pipeline consumes after normalization (§7).

// --- Wire payloads (aliases of the typed apitypes catalog) ---

type (
	SessionStatusPayload = apitypes.SessionStatusPayload
	AuthQRPayload        = apitypes.AuthQRPayload
	AuthCodePayload      = apitypes.AuthCodePayload
	MessagePayload       = apitypes.MessagePayload
	MediaMeta            = domain.MediaMeta
	LocationData         = apitypes.LocationData
	ContactData          = apitypes.ContactData
	PollData             = apitypes.PollData
	MessageStatusPayload = apitypes.MessageStatusPayload
	PresencePayload      = apitypes.PresencePayload
	GroupPayload         = apitypes.GroupPayload
	ChatUpdatePayload    = apitypes.ChatUpdatePayload
	ContactUpdatePayload = apitypes.ContactUpdatePayload
	CallPayload          = apitypes.CallPayload
	NewsletterPayload    = apitypes.NewsletterPayload
)

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

	// Poll vote (incoming PollUpdateMessage). The vote payload is encrypted at
	// normalize time, so only the target poll id is known here; decryption and
	// option-text resolution happen in the composition-layer normalizer (which
	// holds the whatsmeow client + the stored poll options). SelectedOptions is
	// therefore filled in after this struct is produced.
	PollVoteTargetID string   // the poll-creation message id being voted on
	SelectedOptions  []string // resolved selected-option text (empty until resolved)
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
