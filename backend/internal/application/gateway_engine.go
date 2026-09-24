// Package application defines transport-independent use-case boundaries shared
// by the local gateway adapter and future control-plane transports.
package application

import (
	"context"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// AccountPresence is deliberately closed so an unknown wire value cannot be
// silently interpreted as offline.
type AccountPresence string

const (
	AccountPresenceOnline  AccountPresence = "online"
	AccountPresenceOffline AccountPresence = "offline"
)

// SessionStateQuery identifies the gateway-owned session whose live state is
// requested. Authentication and ownership checks remain the caller's job.
type SessionStateQuery struct {
	OrganizationID  string
	SessionID       string
	GatewayID       string
	AssignmentEpoch uint64
}

// SessionState is a point-in-time snapshot from the gateway runtime.
type SessionState struct {
	OrganizationID string
	SessionID      string
	GatewayID      string
	Status         domain.SessionStatus
	Connected      bool
	LoggedIn       bool
}

// SetPresenceCommand requests an account-wide presence mutation.
type SetPresenceCommand struct {
	CommandID       string
	OrganizationID  string
	SessionID       string
	GatewayID       string
	AssignmentEpoch uint64
	State           AccountPresence
}

// MarkReadCommand requests read receipts for one or more messages.
type MarkReadCommand struct {
	CommandID       string
	OrganizationID  string
	SessionID       string
	GatewayID       string
	AssignmentEpoch uint64
	ChatJID         string
	SenderJID       string
	MessageIDs      []string
	ReadAt          time.Time
}

// SendCommand carries one durable outbound message command. Payload is the
// canonical SendRequest body; media stays inline (base64) and is resolved by
// the gateway under its existing bounded limits. CommandID is the stable
// idempotency key: repeating it must return the stored terminal result, never
// a second WhatsApp send.
type SendCommand struct {
	CommandID       string
	OrganizationID  string
	SessionID       string
	GatewayID       string
	AssignmentEpoch uint64
	Payload         domain.SendRequest
}

// MessageOp identifies a message sub-resource operation carried by
// MessageOpCommand. Values mirror the outbound pipeline's op discriminators.
type MessageOp string

const (
	OpReaction MessageOp = "reaction"
	OpEdit     MessageOp = "edit"
	OpRevoke   MessageOp = "revoke"
	OpVote     MessageOp = "vote"
	OpForward  MessageOp = "forward"
)

// MessageOpCommand requests one message sub-resource operation as a durable
// command. CommandID ledger semantics match SendCommand: a repeated id returns
// the stored terminal result instead of re-executing.
type MessageOpCommand struct {
	CommandID       string
	OrganizationID  string
	SessionID       string
	GatewayID       string
	AssignmentEpoch uint64
	Op              MessageOp
	ChatJID         string
	SenderJID       string
	MessageID       string
	Emoji           string
	NewText         string
	Options         []string
	ToJID           string
}

// Definite command-result statuses. Only these are recorded for replay;
// transient and post-dispatch unknowns stay absent so an API retry re-issues
// the command instead of trusting a fabricated failure.
const (
	CommandSent   = "sent"
	CommandFailed = "failed"
)

// ContactLookup is one phone's on-WhatsApp answer. JID is the resolved WhatsApp
// JID (empty when the number is not registered).
type ContactLookup struct {
	Query string
	JID   string
	IsIn  bool
}

// LookupContactCommand queries the live session for on-WhatsApp answers. A
// read: it carries no command_id and never touches the result ledger.
type LookupContactCommand struct {
	OrganizationID  string
	SessionID       string
	GatewayID       string
	AssignmentEpoch uint64
	Phones          []string
}

// ContactJIDCommand addresses one contact JID for a live read or mutation.
// Reads carry no CommandID; mutations do.
type ContactJIDCommand struct {
	CommandID       string
	OrganizationID  string
	SessionID       string
	GatewayID       string
	AssignmentEpoch uint64
	JID             string
	Blocked         bool
}

// GroupMutationCommand requests one group mutation as a durable command.
// CommandID ledger semantics match SendCommand; Kind selects the operation.
type GroupMutationCommand struct {
	CommandID       string
	OrganizationID  string
	SessionID       string
	GatewayID       string
	AssignmentEpoch uint64
	Kind            GroupMutationKind
	GroupJID        string
	Name            string
	Participants    []string
	Action          GroupParticipantChange
	Settings        GroupSettingsUpdate
}

// GroupMutationKind enumerates the durable group operations.
type GroupMutationKind string

const (
	GroupOpCreate             GroupMutationKind = "create"
	GroupOpUpdateSettings     GroupMutationKind = "update_settings"
	GroupOpUpdateParticipants GroupMutationKind = "update_participants"
	GroupOpLeave              GroupMutationKind = "leave"
)

// GroupParticipantChange mirrors the LiveOps participant actions.
type GroupParticipantChange string

const (
	GroupChangeAdd     GroupParticipantChange = "add"
	GroupChangeRemove  GroupParticipantChange = "remove"
	GroupChangePromote GroupParticipantChange = "promote"
	GroupChangeDemote  GroupParticipantChange = "demote"
)

// GroupSettingsUpdate carries optional group settings; nil fields are left
// unchanged, matching domain.GroupSettings semantics on the wire boundary.
type GroupSettingsUpdate struct {
	Subject     *string
	Description *string
	Announce    *bool
	Locked      *bool
}

// toDomainGroupSettings converts the wire settings update into the domain type.
func ToDomainGroupSettings(s GroupSettingsUpdate) domain.GroupSettings {
	return domain.GroupSettings{Subject: s.Subject, Description: s.Description, Announce: s.Announce, Locked: s.Locked}
}

// GroupInfoResult is the live view of a group returned by create flows. The
// API persists its own projections from these raw values.
type GroupInfoResult struct {
	GroupJID     string
	Subject      string
	Description  string
	OwnerJID     string
	Participants int32
	IsAnnounce   bool
	IsLocked     bool
}

// ChatPresenceCommand sets per-chat typing state for a session.
type ChatPresenceCommand struct {
	OrganizationID  string
	SessionID       string
	GatewayID       string
	AssignmentEpoch uint64
	ChatJID         string
	State           string // composing | paused | recording
}

// CommandResultRecord is one definite terminal outcome of a stable engine
// command, stored by the executing gateway for idempotent replay.
type CommandResultRecord struct {
	CommandID   string
	SessionID   string
	Status      string
	WAMessageID string
	Error       string
	UpdatedAt   time.Time
}

// SendMessageResult is one terminal send outcome. A result may be a replayed
// ledger entry for a repeated CommandID; consumers must treat it as evidence
// of at-least-once execution with command-level deduplication, not of exactly-
// once dispatch.
type SendMessageResult struct {
	MutationResult
	WAMessageID string
	SentAt      time.Time
}

// MutationResult preserves routing and fencing metadata across the boundary.
// It is not evidence that ownership was authorized, the epoch matched the
// current assignment, or the command was durably deduplicated.
type MutationResult struct {
	CommandID       string
	OrganizationID  string
	SessionID       string
	GatewayID       string
	AssignmentEpoch uint64
}

// SessionStateReader is the smallest read boundary currently needed by the
// control plane.
type SessionStateReader interface {
	GetSessionState(context.Context, SessionStateQuery) (SessionState, error)
}

// PresenceSetter is the account-presence mutation boundary.
type PresenceSetter interface {
	SetAccountPresence(context.Context, SetPresenceCommand) (MutationResult, error)
}

// ReadMarker is the read-receipt mutation boundary.
type ReadMarker interface {
	MarkRead(context.Context, MarkReadCommand) (MutationResult, error)
}

// MessageSender is the reliable send-command boundary. Implementations own
// command-result deduplication: a repeated CommandID returns the original
// terminal result or an ambiguity error, and never re-dispatches inside the
// supported idempotency window.
type MessageSender interface {
	SendMessage(context.Context, SendCommand) (SendMessageResult, error)
}

// MessageOpResult is one terminal op outcome; WAMessageID carries the
// operation's acknowledgement id when whatsmeow assigned one.
type MessageOpResult struct {
	MutationResult
	SentAt      time.Time
	WAMessageID string
}

// OpExecutor is the reliable message-operation command boundary with the same
// ledger deduplication contract as MessageSender.
type OpExecutor interface {
	ExecuteOp(context.Context, MessageOpCommand) (MessageOpResult, error)
}

// MutationOnlyResult is a terminal mutation outcome carrying routing metadata
// only (no WhatsApp message id).
type MutationOnlyResult struct {
	MutationResult
}

// ContactReader is the live contact-lookup boundary. Reads never write the
// command ledger.
type ContactReader interface {
	LookupContact(context.Context, LookupContactCommand) ([]ContactLookup, error)
	GetContactPicture(ctx context.Context, query SessionStateQuery, jid string) (domain.ProfilePicture, error)
	GetContactAbout(ctx context.Context, query SessionStateQuery, jid string) (string, error)
}

// BlocklistSetter is the block/unblock mutation boundary with full ledger
// deduplication.
type BlocklistSetter interface {
	SetBlocked(context.Context, ContactJIDCommand) (MutationOnlyResult, error)
}

// GroupInfoCarrier is implemented by mutation results that carry raw live
// group metadata for API-side projection.
type GroupInfoCarrier interface {
	Group() GroupInfoResult
}

// GroupCreateResult is one terminal group-mutation outcome; CreatedGroup
// carries the new group's live metadata for create commands (zero otherwise).
type GroupCreateResult struct {
	MutationOnlyResult
	CreatedGroup GroupInfoResult
}

// Group returns the created group's live metadata.
func (r GroupCreateResult) Group() GroupInfoResult { return r.CreatedGroup }

// GroupMutator is the durable group-mutation boundary with full ledger
// deduplication.
type GroupMutator interface {
	MutateGroup(context.Context, GroupMutationCommand) (GroupCreateResult, error)
}

// InviteLinkReader reads (and optionally resets) a group's invite link. The
// reset variant mutates WhatsApp state but stays outside the ledger, mirroring
// the LiveOps surface it serves.
type InviteLinkReader interface {
	GetGroupInviteLink(ctx context.Context, query SessionStateQuery, groupJID string, reset bool) (string, error)
	JoinGroup(ctx context.Context, query SessionStateQuery, invite string) (string, error)
}

// ChatPresenceBoundary is the per-chat presence boundary: subscribe-and-snapshot
// reads and typing-state writes.
type ChatPresenceBoundary interface {
	GetChatPresence(ctx context.Context, query SessionStateQuery, chatJID string) (domain.PresenceStatus, error)
	SetChatPresence(context.Context, ChatPresenceCommand) error
}

// BackfillReader pulls the session's direct-API data snapshot. It is slow; the
// transport applies its send deadline.
type BackfillReader interface {
	BackfillSession(ctx context.Context, query SessionStateQuery) (domain.BackfillSnapshot, error)
}

// PrepareSessionResult echoes the routing metadata of a successful prepare.
// The gateway-local keystore device + managed-session entry it materializes are
// deliberately invisible to the API: only their absence (an error) is.
type PrepareSessionResult struct {
	MutationResult
}

// SessionPreparer is the pairing-substrate boundary: it creates the gateway-
// local keystore device and managed-session entry for an API-created session
// row. Idempotent by construction — preparing an already-known session is a
// success — so no command_id or ledger is involved.
type SessionPreparer interface {
	PrepareSession(context.Context, SessionStateQuery) (PrepareSessionResult, error)
}

// PairingSnapshot is one QR-pairing read outcome. Code empty means no code is
// ready yet; the caller then polls again or subscribes to auth.qr events.
type PairingSnapshot struct {
	Code      string
	ExpiresAt int64 // epoch-ms; 0 = unknown
}

// PairingBeginner starts QR pairing and returns the current snapshot code. A
// repeated call re-reads the live code instead of restarting anything, so it is
// not ledger-backed.
type PairingBeginner interface {
	BeginPairing(ctx context.Context, query SessionStateQuery) (PairingSnapshot, error)
}

// PhonePairer requests a phone-number pairing code. Each call yields a fresh
// one-time secret, so there is nothing durable to replay; not ledger-backed.
type PhonePairer interface {
	PairPhone(ctx context.Context, query SessionStateQuery, phone string) (string, error)
}

// SessionLogout is the destructive logout mutation boundary with full ledger
// deduplication: a repeated CommandID returns the stored terminal outcome
// without unlinking the device twice.
type SessionLogout interface {
	LogoutSession(context.Context, ContactJIDCommand) (MutationOnlyResult, error)
}

// SessionForgetter drops a session's in-memory runtime during the API-owned
// delete flow. The row may already be deleted API-side, so no assignment epoch
// applies — only the target is validated. Idempotent: forgetting an unknown
// session succeeds.
type SessionForgetter interface {
	ForgetSession(ctx context.Context, organizationID, sessionID string) error
}

// GatewayEngine is only the composition of the implemented slices. New
// capabilities should begin as focused consumer-owned ports, not accumulate
// here speculatively.
type GatewayEngine interface {
	SessionStateReader
	PresenceSetter
	ReadMarker
	MessageSender
	OpExecutor
	ContactReader
	BlocklistSetter
	GroupMutator
	InviteLinkReader
	ChatPresenceBoundary
	BackfillReader
	SessionPreparer
	PairingBeginner
	PhonePairer
	SessionLogout
	SessionForgetter
}
