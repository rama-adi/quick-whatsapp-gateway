// Package application defines transport-independent use-case boundaries shared
// by the local gateway adapter and future control-plane transports.
package application

import (
	"context"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
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
	WAMessageID string
}

// OpExecutor is the reliable message-operation command boundary with the same
// ledger deduplication contract as MessageSender.
type OpExecutor interface {
	ExecuteOp(context.Context, MessageOpCommand) (MessageOpResult, error)
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
}
