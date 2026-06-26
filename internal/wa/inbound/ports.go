package inbound

import (
	"context"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// This file declares the CONSUMER INTERFACES the inbound pipeline depends on.
// Per the parallel-build rules and the Go "interfaces defined by the consumer"
// convention, inbound never imports sibling packages (store, wa client, stream,
// webhooks); it depends only on these small interfaces, which Phase 3 satisfies
// with concrete types.

// Normalizer maps a raw whatsmeow event onto the decoupled domain envelope
// (domain.Event) plus the pipeline's working view (NormalizedMessage).
//
// The bool return is "ok": false means the event was filtered/unrecognized and
// the pipeline drops it silently (e.g. source-level ignore rules in §7, or an
// event type we don't model). When ok is true, both the Event and the
// *NormalizedMessage are non-nil and consistent.
type Normalizer interface {
	Normalize(evt any, sessionID, organizationID string) (domain.Event, *NormalizedMessage, bool)
}

// CommandRegistry handles admin-session private commands (§6/§7). On the admin
// session, an inbound text whose body starts with the configured prefix is
// handed here and DROPPED from the rest of the pipeline (not persisted, not
// emitted, not counted as a contact).
//
// handled=true means the registry consumed the command (the pipeline drops the
// event regardless of err, since a prefixed admin command is never a normal
// message). handled=false means "not a command I recognize" — but note the
// interceptor still drops any prefix-matching admin message in v1 (the no-op
// registry returns handled=false); see Pipeline docs.
type CommandRegistry interface {
	Handle(ctx context.Context, sessionID, body string) (handled bool, err error)
}

// EventSink publishes the versioned envelope to live stream subscribers
// (Redis pub/sub fan-out in Phase 3).
type EventSink interface {
	Publish(ctx context.Context, evt domain.Event) error
}

// WebhookEnqueuer enqueues webhook deliveries for an event.
type WebhookEnqueuer interface {
	Enqueue(ctx context.Context, evt domain.Event) error
}

// WAClient is the subset of the whatsmeow client wrapper the pipeline calls
// during the auto-read stage. SessionID identifies which managed client to use,
// since one pipeline serves many sessions.
type WAClient interface {
	// SendReadReceipt marks the given messages in chatJID (from senderJID) read.
	SendReadReceipt(ctx context.Context, sessionID, chatJID, senderJID string, messageIDs []string) error
	// SendPresence sends a chat presence state ("composing"/"paused") to chatJID.
	SendPresence(ctx context.Context, sessionID, chatJID, state string) error
}

// Clock abstracts time for deterministic tests. Production wires domain.NowMs.
type Clock interface {
	NowMs() int64
}

// Repos is the subset of store methods the pipeline calls. It is intentionally
// one interface (the pipeline always gets a single repo facade) but groups the
// per-table upserts the capture/persist stages need. All methods take ctx first
// and are expected to be idempotent upserts keyed by the natural keys in §5.
type Repos interface {
	// --- identity / contacts capture (§7.3) ---

	// UpsertIdentity records/refreshes global LID->phone/name resolution.
	UpsertIdentity(ctx context.Context, in IdentityUpsert) error
	// UpsertContact records that sessionID encountered lid; bumps message_count
	// and last_seen, and (for DMs) sets seen_in_dm + DM timestamps.
	UpsertContact(ctx context.Context, in ContactUpsert) error
	// UpsertGroup records/refreshes group metadata.
	UpsertGroup(ctx context.Context, in GroupUpsert) error
	// UpsertGroupMember records a participant's membership (nickname + role).
	UpsertGroupMember(ctx context.Context, in GroupMemberUpsert) error

	// --- persist (§7.4) ---

	// UpsertChat records/refreshes the chat row and last_message_at.
	UpsertChat(ctx context.Context, in ChatUpsert) error
	// InsertMessage inserts a message row (idempotent on session_id+wa_message_id).
	InsertMessage(ctx context.Context, in MessageInsert) error
	// MarkMessageEdited / MarkMessageDeleted flip the edited/deleted flags.
	MarkMessageEdited(ctx context.Context, sessionID, waMessageID, newBody string) error
	MarkMessageDeleted(ctx context.Context, sessionID, waMessageID string) error
	// UpdateMessageStatus updates status/ack_level for acked messages (receipts).
	UpdateMessageStatus(ctx context.Context, in MessageStatusUpdate) error
	// InsertPollVote inserts a poll_votes row.
	InsertPollVote(ctx context.Context, in PollVoteInsert) error

	// --- fan-out (§7.6) ---

	// AppendEventLog appends to event_log for cursor-based stream resume.
	AppendEventLog(ctx context.Context, evt domain.Event) error
}

// --- repo argument structs (decoupled from store's row types) ---

// IdentityUpsert is the input to Repos.UpsertIdentity.
type IdentityUpsert struct {
	LID          string
	PhoneNumber  string
	PhoneJID     string
	Name         string // push name
	BusinessName string
	NowMs        int64
}

// ContactUpsert is the input to Repos.UpsertContact.
type ContactUpsert struct {
	SessionID string
	LID       string
	// SeenInDM, when true, sets seen_in_dm and the DM first/last-seen timestamps.
	SeenInDM bool
	// BumpMessageCount adds one to message_count (only for real messages, not
	// receipts/presence).
	BumpMessageCount bool
	NowMs            int64
}

// GroupUpsert is the input to Repos.UpsertGroup.
type GroupUpsert struct {
	GroupJID         string
	Subject          string
	Description      string
	OwnerJID         string
	ParticipantCount *int
	IsAnnounce       *bool
	IsLocked         *bool
	CreatedAtWA      *int64
	NowMs            int64
}

// GroupMemberUpsert is the input to Repos.UpsertGroupMember.
type GroupMemberUpsert struct {
	SessionID string
	GroupJID  string
	LID       string
	Nickname  string
	Role      domain.GroupRole
	NowMs     int64
}

// ChatUpsert is the input to Repos.UpsertChat.
type ChatUpsert struct {
	SessionID     string
	ChatJID       string
	Type          domain.ChatType
	Name          string
	LastMessageAt int64
	NowMs         int64
}

// MessageInsert is the input to Repos.InsertMessage.
type MessageInsert struct {
	SessionID       string
	WAMessageID     string
	ChatJID         string
	SenderLID       string
	SenderJID       string
	FromMe          bool
	Direction       domain.MessageDirection
	Type            string
	Body            string
	QuotedMessageID string
	Mentions        []string
	HasMedia        bool
	MediaMeta       *domain.MediaMeta
	TimestampMs     int64
	RawJSON         []byte
	NowMs           int64
}

// MessageStatusUpdate is the input to Repos.UpdateMessageStatus.
type MessageStatusUpdate struct {
	SessionID    string
	WAMessageIDs []string
	Status       domain.MessageStatus
	AckLevel     *int
	NowMs        int64
}

// PollVoteInsert is the input to Repos.InsertPollVote.
type PollVoteInsert struct {
	SessionID       string
	PollMessageID   string
	VoterLID        string
	SelectedOptions []byte
	TimestampMs     int64
	RawJSON         []byte
}
