package service

import (
	"context"

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

// errLiveUnavailable is returned by the resource services when no live adapter is
// configured (e.g. in tests or before the client is wired).
func errLiveUnavailable() error {
	return domain.ErrNotImplemented("live WhatsApp client is not available for this session")
}
