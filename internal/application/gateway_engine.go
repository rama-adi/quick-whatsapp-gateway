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

// GatewayEngine is only the composition of the three implemented slices. New
// capabilities should begin as focused consumer-owned ports, not accumulate
// here speculatively.
type GatewayEngine interface {
	SessionStateReader
	PresenceSetter
	ReadMarker
}
