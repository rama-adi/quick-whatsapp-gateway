package wa

import (
	"context"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// This file exposes a manager-backed adapter for the "live ops" the resource
// services need (group management, on-WhatsApp checks, profile picture/about,
// presence, channels). The adapter resolves the per-session *whatsmeow.Client
// and translates between the gateway's string-JID API surface and whatsmeow's
// typed calls (recon §8/§9).
//
// The service package defines the port interfaces (consumer-defines convention);
// this adapter satisfies them structurally — there is no import of the service
// package here, so there is no cycle. The exported helper value types mirror the
// service ones field-for-field.

// liveClient is the slice of *whatsmeow.Client the live-ops adapter drives. The
// real client satisfies it; it is intentionally separate from waClient so the
// lifecycle fake in tests does not need to implement these methods.
type liveClient interface {
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
	CreateGroup(ctx context.Context, req whatsmeow.ReqCreateGroup) (*types.GroupInfo, error)
	UpdateGroupParticipants(ctx context.Context, jid types.JID, participants []types.JID, action whatsmeow.ParticipantChange) ([]types.GroupParticipant, error)
	SetGroupName(ctx context.Context, jid types.JID, name string) error
	SetGroupTopic(ctx context.Context, jid types.JID, previousID, newID, topic string) error
	SetGroupAnnounce(ctx context.Context, jid types.JID, announce bool) error
	SetGroupLocked(ctx context.Context, jid types.JID, locked bool) error
	GetGroupInviteLink(ctx context.Context, jid types.JID, reset bool) (string, error)
	JoinGroupWithLink(ctx context.Context, code string) (types.JID, error)
	LeaveGroup(ctx context.Context, jid types.JID) error
	IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
	GetProfilePictureInfo(ctx context.Context, jid types.JID, params *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error)
	GetUserInfo(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error)
	UpdateBlocklist(ctx context.Context, jid types.JID, action events.BlocklistChangeAction) (*types.Blocklist, error)
	SendPresence(ctx context.Context, state types.Presence) error
	SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error
}

// LiveOps returns a session-resolving adapter over the manager. It satisfies the
// service package's GroupOps / ContactDirectory / PresenceController / ChannelOps
// ports structurally. Wire it into the resource services in the composition root.
func (m *Manager) LiveOps() *LiveOps { return &LiveOps{m: m} }

// LiveOps adapts the manager's per-session whatsmeow clients to the resource
// services' live-ops ports.
type LiveOps struct{ m *Manager }

// client resolves the connected live client for a session, or an error mapped to
// the API not_implemented/unavailable envelope when the session is not connected.
func (l *LiveOps) client(id string) (liveClient, error) {
	ms := l.m.Get(id)
	if ms == nil {
		return nil, domain.ErrNotFound("session not found")
	}
	ms.mu.Lock()
	c := ms.client
	ms.mu.Unlock()
	if c == nil {
		return nil, domain.ErrNotImplemented("live WhatsApp client is not available for this session")
	}
	lc, ok := c.(liveClient)
	if !ok {
		return nil, domain.ErrNotImplemented("live WhatsApp client is not available for this session")
	}
	return lc, nil
}

func parseJID(s string) (types.JID, error) {
	jid, err := types.ParseJID(s)
	if err != nil {
		return types.JID{}, domain.ErrValidation("invalid jid: " + s)
	}
	return jid, nil
}

func parseJIDs(ss []string) ([]types.JID, error) {
	out := make([]types.JID, 0, len(ss))
	for _, s := range ss {
		j, err := parseJID(s)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// ---- GroupOps ----

func toGroupInfo(g *types.GroupInfo) domain.GroupInfo {
	if g == nil {
		return domain.GroupInfo{}
	}
	return domain.GroupInfo{
		GroupJID:     g.JID.String(),
		Subject:      g.Name,
		Description:  g.Topic,
		OwnerJID:     g.OwnerJID.String(),
		Participants: len(g.Participants),
		IsAnnounce:   g.IsAnnounce,
		IsLocked:     g.IsLocked,
	}
}

// CreateGroup creates a group.
func (l *LiveOps) CreateGroup(ctx context.Context, sessionID, name string, participants []string) (domain.GroupInfo, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	jids, err := parseJIDs(participants)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	g, err := c.CreateGroup(ctx, whatsmeow.ReqCreateGroup{Name: name, Participants: jids})
	if err != nil {
		return domain.GroupInfo{}, err
	}
	return toGroupInfo(g), nil
}

// GetGroupInfo fetches live group metadata.
func (l *LiveOps) GetGroupInfo(ctx context.Context, sessionID, groupJID string) (domain.GroupInfo, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	jid, err := parseJID(groupJID)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	g, err := c.GetGroupInfo(ctx, jid)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	return toGroupInfo(g), nil
}

// UpdateParticipants applies an add/remove/promote/demote.
func (l *LiveOps) UpdateParticipants(ctx context.Context, sessionID, groupJID string, participants []string, action domain.GroupParticipantAction) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	jid, err := parseJID(groupJID)
	if err != nil {
		return err
	}
	jids, err := parseJIDs(participants)
	if err != nil {
		return err
	}
	var pc whatsmeow.ParticipantChange
	switch action {
	case domain.GroupActionAdd:
		pc = whatsmeow.ParticipantChangeAdd
	case domain.GroupActionRemove:
		pc = whatsmeow.ParticipantChangeRemove
	case domain.GroupActionPromote:
		pc = whatsmeow.ParticipantChangePromote
	case domain.GroupActionDemote:
		pc = whatsmeow.ParticipantChangeDemote
	default:
		return domain.ErrValidation("invalid participant action")
	}
	_, err = c.UpdateGroupParticipants(ctx, jid, jids, pc)
	return err
}

// UpdateSettings applies subject/description/announce/locked.
func (l *LiveOps) UpdateSettings(ctx context.Context, sessionID, groupJID string, s domain.GroupSettings) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	jid, err := parseJID(groupJID)
	if err != nil {
		return err
	}
	if s.Subject != nil {
		if err := c.SetGroupName(ctx, jid, *s.Subject); err != nil {
			return err
		}
	}
	if s.Description != nil {
		if err := c.SetGroupTopic(ctx, jid, "", "", *s.Description); err != nil {
			return err
		}
	}
	if s.Announce != nil {
		if err := c.SetGroupAnnounce(ctx, jid, *s.Announce); err != nil {
			return err
		}
	}
	if s.Locked != nil {
		if err := c.SetGroupLocked(ctx, jid, *s.Locked); err != nil {
			return err
		}
	}
	return nil
}

// GetInviteLink returns the group invite link (reset=true revokes+regenerates).
func (l *LiveOps) GetInviteLink(ctx context.Context, sessionID, groupJID string, reset bool) (string, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return "", err
	}
	jid, err := parseJID(groupJID)
	if err != nil {
		return "", err
	}
	return c.GetGroupInviteLink(ctx, jid, reset)
}

// JoinWithLink joins a group from an invite code/link.
func (l *LiveOps) JoinWithLink(ctx context.Context, sessionID, code string) (string, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return "", err
	}
	jid, err := c.JoinGroupWithLink(ctx, code)
	if err != nil {
		return "", err
	}
	return jid.String(), nil
}

// Leave leaves a group.
func (l *LiveOps) Leave(ctx context.Context, sessionID, groupJID string) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	jid, err := parseJID(groupJID)
	if err != nil {
		return err
	}
	return c.LeaveGroup(ctx, jid)
}

// ---- ContactDirectory ----

// IsOnWhatsApp checks phone numbers.
func (l *LiveOps) IsOnWhatsApp(ctx context.Context, sessionID string, phones []string) ([]domain.OnWhatsApp, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return nil, err
	}
	res, err := c.IsOnWhatsApp(ctx, phones)
	if err != nil {
		return nil, err
	}
	out := make([]domain.OnWhatsApp, 0, len(res))
	for _, r := range res {
		out = append(out, domain.OnWhatsApp{Query: r.Query, JID: r.JID.String(), IsIn: r.IsIn})
	}
	return out, nil
}

// ProfilePicture returns a contact's profile picture (nil-safe: WhatsApp returns
// (nil,nil) when hidden — mapped to an empty result).
func (l *LiveOps) ProfilePicture(ctx context.Context, sessionID, jid string) (domain.ProfilePicture, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return domain.ProfilePicture{}, err
	}
	j, err := parseJID(jid)
	if err != nil {
		return domain.ProfilePicture{}, err
	}
	info, err := c.GetProfilePictureInfo(ctx, j, nil)
	if err != nil {
		return domain.ProfilePicture{}, err
	}
	if info == nil {
		return domain.ProfilePicture{}, nil
	}
	return domain.ProfilePicture{URL: info.URL, ID: info.ID}, nil
}

// About returns a contact's status text.
func (l *LiveOps) About(ctx context.Context, sessionID, jid string) (string, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return "", err
	}
	j, err := parseJID(jid)
	if err != nil {
		return "", err
	}
	info, err := c.GetUserInfo(ctx, []types.JID{j})
	if err != nil {
		return "", err
	}
	if ui, ok := info[j]; ok {
		return ui.Status, nil
	}
	return "", nil
}

// SetBlocked blocks/unblocks a contact.
func (l *LiveOps) SetBlocked(ctx context.Context, sessionID, jid string, blocked bool) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	j, err := parseJID(jid)
	if err != nil {
		return err
	}
	action := events.BlocklistChangeActionUnblock
	if blocked {
		action = events.BlocklistChangeActionBlock
	}
	_, err = c.UpdateBlocklist(ctx, j, action)
	return err
}

// ---- PresenceController ----

// SetPresence sets account-wide presence.
func (l *LiveOps) SetPresence(ctx context.Context, sessionID, state string) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	p := types.PresenceUnavailable
	if state == "online" {
		p = types.PresenceAvailable
	}
	return c.SendPresence(ctx, p)
}

// SetChatPresence sets per-chat typing state.
func (l *LiveOps) SetChatPresence(ctx context.Context, sessionID, chatJID, state string) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	j, err := parseJID(chatJID)
	if err != nil {
		return err
	}
	var (
		cp    types.ChatPresence
		media types.ChatPresenceMedia
	)
	switch state {
	case "composing":
		cp = types.ChatPresenceComposing
	case "paused":
		cp = types.ChatPresencePaused
	case "recording":
		cp = types.ChatPresenceComposing
		media = types.ChatPresenceMediaAudio
	default:
		return domain.ErrValidation("invalid chat presence state")
	}
	return c.SendChatPresence(ctx, j, cp, media)
}

// ---- ChannelOps ----
//
// whatsmeow's newsletter API surface (CreateNewsletter / FollowNewsletter /
// UnfollowNewsletter / NewsletterToggleMute) is not part of the narrow live
// client wired for v1; channels are reported as not_implemented consistently
// with the media send types.

// Create is not implemented in v1.
func (l *LiveOps) Create(ctx context.Context, sessionID, name, description string) (string, error) {
	return "", domain.ErrNotImplemented("channel create is not implemented yet")
}

// Follow is not implemented in v1.
func (l *LiveOps) Follow(ctx context.Context, sessionID, jid string) error {
	return domain.ErrNotImplemented("channel follow is not implemented yet")
}

// Unfollow is not implemented in v1.
func (l *LiveOps) Unfollow(ctx context.Context, sessionID, jid string) error {
	return domain.ErrNotImplemented("channel unfollow is not implemented yet")
}

// Mute is not implemented in v1.
func (l *LiveOps) Mute(ctx context.Context, sessionID, jid string, mute bool) error {
	return domain.ErrNotImplemented("channel mute is not implemented yet")
}

// ---- StatusPoster ----
//
// Status posting uses SendMessage to the status broadcast JID; that path goes
// through the outbound Sender (currently stubbed), so it is reported as
// not_implemented here until the live client is wired end-to-end.

// PostText is not implemented in v1.
func (l *LiveOps) PostText(ctx context.Context, sessionID, text string) (string, error) {
	return "", domain.ErrNotImplemented("status posting is not implemented yet")
}
