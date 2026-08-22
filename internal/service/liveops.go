package service

import (
	"context"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// This file defines the narrow "live ops" ports the resource services depend on
// for operations that must hit a connected whatsmeow client (group management,
// on-WhatsApp checks, profile picture / about, presence, channels, status).
//
// Per the consumer-defines-the-interface convention, the ports live here in the
// service package. A manager-backed adapter (internal/wa.LiveOps) satisfies them
// in production; tests inject fakes. The exchanged value types live in the domain
// package (domain.GroupInfo, domain.OnWhatsApp, …) so both this package and the
// wa adapter reference identical types without an import cycle.
//
// Until the per-session whatsmeow client is wired end-to-end (the Sender
// currently uses a stub client), some adapter calls return
// domain.ErrNotImplemented — the service/handler plumbing, validation and tests
// are complete regardless.

// Re-exported aliases so callers within the service package read naturally.
type (
	GroupParticipantAction = domain.GroupParticipantAction
	GroupInfo              = domain.GroupInfo
	GroupSettings          = domain.GroupSettings
	OnWhatsApp             = domain.OnWhatsApp
	ProfilePicture         = domain.ProfilePicture
)

const (
	GroupActionAdd     = domain.GroupActionAdd
	GroupActionRemove  = domain.GroupActionRemove
	GroupActionPromote = domain.GroupActionPromote
	GroupActionDemote  = domain.GroupActionDemote
)

// GroupOps is the live group-management surface (§11 Groups).
type GroupOps interface {
	CreateGroup(ctx context.Context, sessionID, name string, participants []string) (GroupInfo, error)
	GetGroupInfo(ctx context.Context, sessionID, groupJID string) (GroupInfo, error)
	UpdateParticipants(ctx context.Context, sessionID, groupJID string, participants []string, action GroupParticipantAction) error
	UpdateSettings(ctx context.Context, sessionID, groupJID string, s GroupSettings) error
	GetInviteLink(ctx context.Context, sessionID, groupJID string, reset bool) (string, error)
	JoinWithLink(ctx context.Context, sessionID, code string) (groupJID string, err error)
	Leave(ctx context.Context, sessionID, groupJID string) error
}

// ContactDirectory is the live contact-lookup surface (§11 Contacts live calls).
type ContactDirectory interface {
	IsOnWhatsApp(ctx context.Context, sessionID string, phones []string) ([]OnWhatsApp, error)
	ProfilePicture(ctx context.Context, sessionID, jid string) (ProfilePicture, error)
	About(ctx context.Context, sessionID, jid string) (status string, err error)
	SetBlocked(ctx context.Context, sessionID, jid string, blocked bool) error
}

// PresenceController is the live presence surface (§11 Presence / chat presence).
type PresenceController interface {
	// SetPresence sets the account-wide presence: "online" or "offline".
	SetPresence(ctx context.Context, sessionID, state string) error
	// SetChatPresence sets the per-chat typing state: composing/paused/recording.
	SetChatPresence(ctx context.Context, sessionID, chatJID, state string) error
	// GetPresence subscribes to a contact's presence updates and returns the
	// current REST snapshot. WhatsApp may not send a concrete state until a later
	// presence.update event arrives.
	GetPresence(ctx context.Context, sessionID, chatJID string) (domain.PresenceStatus, error)
}

// GatewayLiveFacade is the API-facing, resolved live-operation port. Its
// implementation owns session-to-gateway resolution and assignment fencing;
// REST services retain authorization and public-input validation only.
type GatewayLiveFacade interface {
	GetSessionState(context.Context, string, string) (application.SessionState, error)
	SetAccountPresence(context.Context, string, string, application.AccountPresence) error
}

// GatewayContactFacade is the API-facing contact live-operation port (§11
// Contacts live calls). Implementations resolve the assigned engine per call.
type GatewayContactFacade interface {
	LookupContact(ctx context.Context, organizationID, sessionID string, phones []string) ([]domain.OnWhatsApp, error)
	GetContactPicture(ctx context.Context, organizationID, sessionID, jid string) (domain.ProfilePicture, error)
	GetContactAbout(ctx context.Context, organizationID, sessionID, jid string) (string, error)
	SetBlocked(ctx context.Context, organizationID, sessionID, jid string, blocked bool) error
}

// GatewayGroupFacade is the API-facing group live-operation port (§11 Groups).
// Mutations run as durable engine commands; the service persists projections
// from the raw results.
type GatewayGroupFacade interface {
	CreateGroup(ctx context.Context, organizationID, sessionID, name string, participants []string) (domain.GroupInfo, error)
	UpdateParticipants(ctx context.Context, organizationID, sessionID, groupJID string, participants []string, action GroupParticipantAction) error
	UpdateSettings(ctx context.Context, organizationID, sessionID, groupJID string, s GroupSettings) error
	GetInviteLink(ctx context.Context, organizationID, sessionID, groupJID string, reset bool) (string, error)
	JoinWithLink(ctx context.Context, organizationID, sessionID, code string) (groupJID string, err error)
	Leave(ctx context.Context, organizationID, sessionID, groupJID string) error
}

// GatewayChatFacade is the API-facing chat-presence port (§11 Chats presence).
type GatewayChatFacade interface {
	GetChatPresence(ctx context.Context, organizationID, sessionID, chatJID string) (domain.PresenceStatus, error)
	SetChatPresence(ctx context.Context, organizationID, sessionID, chatJID, state string) error
}

// GatewayBackfillFacade is the API-facing admin-backfill port. It returns the
// raw live snapshot; the admin service persists projections itself.
type GatewayBackfillFacade interface {
	BackfillSessionData(ctx context.Context, organizationID, sessionID string) (domain.BackfillSnapshot, error)
}

// GatewaySessionFacade is the API-facing session-lifecycle port (Increment 7):
// the five live engine calls behind create/QR/pairing/logout/delete. Row,
// placement, and assignment ownership stays with the SessionService; an empty
// PairingSnapshot means no QR code is ready yet (the caller polls or subscribes
// to auth.qr events).
type GatewaySessionFacade interface {
	Prepare(ctx context.Context, organizationID, sessionID string) error
	QR(ctx context.Context, organizationID, sessionID string) (application.PairingSnapshot, error)
	PairingCode(ctx context.Context, organizationID, sessionID, phone string) (string, error)
	Logout(ctx context.Context, organizationID, sessionID string) error
	Forget(ctx context.Context, organizationID, sessionID string) error
}

// ChannelOps is the live channel/newsletter surface (§11 Channels).
type ChannelOps interface {
	Create(ctx context.Context, sessionID, name, description string) (jid string, err error)
	Follow(ctx context.Context, sessionID, jid string) error
	Unfollow(ctx context.Context, sessionID, jid string) error
	Mute(ctx context.Context, sessionID, jid string, mute bool) error
}

// StatusPoster posts a text status/story (§11 Status; media => not_implemented).
type StatusPoster interface {
	PostText(ctx context.Context, sessionID, text string) (messageID string, err error)
}

// BackfillSource pulls currently supported live data for a session.
type BackfillSource interface {
	BackfillSessionData(ctx context.Context, sessionID string) (domain.BackfillSnapshot, error)
}

// errLiveUnavailable is returned by the resource services when no live adapter is
// configured (e.g. in tests or before the client is wired).
func errLiveUnavailable() error {
	return domain.ErrNotImplemented("live WhatsApp client is not available for this session")
}
