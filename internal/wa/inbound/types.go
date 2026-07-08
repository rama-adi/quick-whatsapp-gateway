package inbound

import (
	"encoding/json"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// NormalizedMessage is the decoupled, transport-free view of a single inbound
// whatsmeow event that the pipeline operates on after the Normalize stage.
//
// WHY this lives here (and not in internal/wa/events): the inbound package is the
// CONSUMER of normalization. Per the Go "interfaces defined by the consumer"
// convention and the parallel-build import rules, inbound owns the contract it
// needs and the Normalizer implementation (in internal/wa/events) maps a raw
// whatsmeow event onto this struct. The pipeline never touches raw protobufs.
//
// A NormalizedMessage describes WHAT happened (Kind) and carries only the fields
// relevant to that kind; irrelevant fields are zero/nil. The matching
// domain.Event (the versioned envelope) is produced alongside it by the
// Normalizer and is what gets fanned out — NormalizedMessage drives the
// capture/persist side-effects.
type NormalizedMessage struct {
	// Kind selects which capture/persist path runs. See MessageKind constants.
	Kind MessageKind

	// SessionID / OrganizationID tag the originating WhatsApp session and its organization.
	SessionID      string
	OrganizationID string

	// ChatJID is the conversation JID (a DM peer, a group, a newsletter, …).
	ChatJID string
	// ChatType classifies the chat for the chats table + DM-vs-group capture.
	ChatType domain.ChatType
	// ChatName is the best display name for the chat (group subject / push name).
	ChatName string

	// IsDM / IsGroup are convenience flags derived from ChatType by the
	// Normalizer. Exactly capture logic branches on these.
	IsDM    bool
	IsGroup bool

	// FromMe is true for outbound echoes (own-number sends, §18.2).
	FromMe bool

	// --- sender / identity ---
	SelfJID      string // this session's own phone JID, when known
	SelfLID      string // this session's own LID, when known
	SenderLID    string // LID identity of the sender (preferred key)
	SenderJID    string // phone JID of the sender, when known
	SenderPhone  string // bare phone number, when resolvable
	PushName     string // sender push name (preferred display, §7.3)
	BusinessName string // sender business name, when known

	// --- message body (Kind == KindMessage / KindEdit / KindRevoke) ---
	WAMessageID     string
	MsgType         string // text,poll,location,contact,reaction,system,image,…
	Body            string
	QuotedMessageID string
	Mentions        []string // mentioned JID strings
	HasMedia        bool
	MediaMeta       *domain.MediaMeta
	TimestampMs     int64 // event timestamp (epoch ms)

	// --- group capture (IsGroup) ---
	Group   *NormalizedGroup
	Members []NormalizedMember

	// --- poll creation (Kind == KindMessage, MsgType == "poll") ---
	// Set when the message is a poll creation; persisted to the polls table so
	// later votes can be resolved back to option text.
	Poll *NormalizedPoll

	// --- receipt (Kind == KindReceipt) ---
	Receipt *NormalizedReceipt

	// --- poll vote (Kind == KindPollVote) ---
	PollVote *NormalizedPollVote

	// RawJSON is the normalized event payload persisted to messages.raw_json /
	// poll_votes.raw_json. It is the same shape published in the envelope.
	RawJSON json.RawMessage
}

// MessageKind selects the pipeline path for a NormalizedMessage.
type MessageKind string

const (
	// KindMessage is an inbound or echoed chat message (text/poll/location/…)
	// that should be captured, persisted and fanned out.
	KindMessage MessageKind = "message"
	// KindReceipt is a delivery/read/played ack that updates an existing
	// message's status/ack_level — no new message row.
	KindReceipt MessageKind = "receipt"
	// KindPollVote is a decrypted poll vote that inserts a poll_votes row.
	KindPollVote MessageKind = "poll_vote"
	// KindEdit / KindRevoke mutate an existing message (edited/deleted flags).
	KindEdit   MessageKind = "edit"
	KindRevoke MessageKind = "revoke"
	// KindOther is anything that is fanned out but needs no message-table
	// capture (presence, group.update, chat.update, …). The pipeline still
	// captures identity/contacts when sender info is present.
	KindOther MessageKind = "other"
)

// NormalizedGroup is the group metadata captured for whatsapp_groups upserts.
type NormalizedGroup struct {
	GroupJID         string
	Subject          string
	Description      string
	OwnerJID         string
	ParticipantCount *int
	IsAnnounce       *bool
	IsLocked         *bool
	CreatedAtWA      *int64
}

// NormalizedMember is one group participant for whatsapp_group_members upserts.
type NormalizedMember struct {
	LID  string
	JID  string
	Tag  string // per-group member tag (pivot)
	Role domain.GroupRole
}

// NormalizedReceipt carries the fields needed to update message status/ack.
type NormalizedReceipt struct {
	// MessageIDs are the wa_message_ids the receipt acks.
	MessageIDs []string
	// Status is the new message status (delivered/read/played).
	Status domain.MessageStatus
	// AckLevel is the numeric ack level, when known.
	AckLevel *int
	// TimestampMs is the receipt time.
	TimestampMs int64
}

// NormalizedPoll carries a poll-creation message's options for a polls upsert.
type NormalizedPoll struct {
	Name            string
	Options         []string
	SelectableCount int
	EndTime         int64
	HideVotes       bool
}

// NormalizedPollVote carries the fields for a poll_votes insert.
type NormalizedPollVote struct {
	PollMessageID   string
	VoterLID        string
	SelectedOptions json.RawMessage
	TimestampMs     int64
}
