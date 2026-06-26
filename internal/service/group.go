package service

import (
	"context"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// GroupService backs the group-management endpoints (§11 Groups). Reads
// (list/get/members) are served from the store; mutations and invite links go
// through the live GroupOps surface.
type GroupService struct {
	store *store.Store
	ops   GroupOps
	log   *slog.Logger
}

// NewGroupService constructs a GroupService. ops may be nil (live mutations then
// report the client as unavailable).
func NewGroupService(s *store.Store, ops GroupOps, log *slog.Logger) *GroupService {
	if log == nil {
		log = slog.Default()
	}
	return &GroupService{store: s, ops: ops, log: log}
}

func (s *GroupService) requireSession(ctx context.Context, organizationID, sessionID string) error {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.OrganizationID != organizationID {
		return domain.ErrNotFound("session not found")
	}
	return nil
}

func (s *GroupService) live(ctx context.Context, organizationID, sessionID string) error {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return err
	}
	if s.ops == nil {
		return errLiveUnavailable()
	}
	return nil
}

// Create creates a new group (§11 POST /groups).
func (s *GroupService) Create(ctx context.Context, organizationID, sessionID, name string, participants []string) (GroupInfo, error) {
	if name == "" {
		return GroupInfo{}, domain.ErrValidation("name is required")
	}
	if err := s.live(ctx, organizationID, sessionID); err != nil {
		return GroupInfo{}, err
	}
	return s.ops.CreateGroup(ctx, sessionID, name, participants)
}

// List returns the session's known groups (store-backed; cross-session groups
// share the global whatsapp_groups table, so this lists by membership).
func (s *GroupService) List(ctx context.Context, organizationID, sessionID string) ([]domain.Group, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return nil, err
	}
	return s.store.Groups.ListBySession(ctx, sessionID)
}

// Get returns a group's stored metadata.
func (s *GroupService) Get(ctx context.Context, organizationID, sessionID, groupJID string) (domain.Group, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return domain.Group{}, err
	}
	return s.store.Groups.GetByJID(ctx, groupJID)
}

// Members lists a group's members with role + per-group nickname.
func (s *GroupService) Members(ctx context.Context, organizationID, sessionID, groupJID string) ([]domain.GroupMember, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return nil, err
	}
	return s.store.GroupMembers.ListByGroup(ctx, sessionID, groupJID)
}

// participants applies an add/remove/promote/demote action.
func (s *GroupService) participants(ctx context.Context, organizationID, sessionID, groupJID string, jids []string, action GroupParticipantAction) error {
	if len(jids) == 0 {
		return domain.ErrValidation("at least one participant is required")
	}
	if err := s.live(ctx, organizationID, sessionID); err != nil {
		return err
	}
	return s.ops.UpdateParticipants(ctx, sessionID, groupJID, jids, action)
}

// AddMembers adds participants (§11 POST /groups/{gid}/members).
func (s *GroupService) AddMembers(ctx context.Context, organizationID, sessionID, groupJID string, jids []string) error {
	return s.participants(ctx, organizationID, sessionID, groupJID, jids, GroupActionAdd)
}

// RemoveMember removes one participant (§11 DELETE /groups/{gid}/members/{jid}).
func (s *GroupService) RemoveMember(ctx context.Context, organizationID, sessionID, groupJID, jid string) error {
	return s.participants(ctx, organizationID, sessionID, groupJID, []string{jid}, GroupActionRemove)
}

// Promote makes a member an admin.
func (s *GroupService) Promote(ctx context.Context, organizationID, sessionID, groupJID, jid string) error {
	return s.participants(ctx, organizationID, sessionID, groupJID, []string{jid}, GroupActionPromote)
}

// Demote removes a member's admin role.
func (s *GroupService) Demote(ctx context.Context, organizationID, sessionID, groupJID, jid string) error {
	return s.participants(ctx, organizationID, sessionID, groupJID, []string{jid}, GroupActionDemote)
}

// UpdateSettings applies subject/description/announce/locked (§11 PATCH /groups/{gid}).
func (s *GroupService) UpdateSettings(ctx context.Context, organizationID, sessionID, groupJID string, in GroupSettings) error {
	if in.Subject == nil && in.Description == nil && in.Announce == nil && in.Locked == nil {
		return domain.ErrValidation("no group settings to update")
	}
	if err := s.live(ctx, organizationID, sessionID); err != nil {
		return err
	}
	return s.ops.UpdateSettings(ctx, sessionID, groupJID, in)
}

// InviteLink returns the group's invite link (§11 GET /groups/{gid}/invite).
func (s *GroupService) InviteLink(ctx context.Context, organizationID, sessionID, groupJID string) (string, error) {
	if err := s.live(ctx, organizationID, sessionID); err != nil {
		return "", err
	}
	return s.ops.GetInviteLink(ctx, sessionID, groupJID, false)
}

// RevokeInvite resets the invite link, returning the new one (§11 DELETE /groups/{gid}/invite).
func (s *GroupService) RevokeInvite(ctx context.Context, organizationID, sessionID, groupJID string) (string, error) {
	if err := s.live(ctx, organizationID, sessionID); err != nil {
		return "", err
	}
	return s.ops.GetInviteLink(ctx, sessionID, groupJID, true)
}

// Join joins a group from an invite code/link (§11 POST /groups:join).
func (s *GroupService) Join(ctx context.Context, organizationID, sessionID, invite string) (string, error) {
	if invite == "" {
		return "", domain.ErrValidation("invite is required")
	}
	if err := s.live(ctx, organizationID, sessionID); err != nil {
		return "", err
	}
	return s.ops.JoinWithLink(ctx, sessionID, invite)
}

// Leave leaves a group (§11 POST /groups/{gid}:leave).
func (s *GroupService) Leave(ctx context.Context, organizationID, sessionID, groupJID string) error {
	if err := s.live(ctx, organizationID, sessionID); err != nil {
		return err
	}
	return s.ops.Leave(ctx, sessionID, groupJID)
}

// ApproveMembers approves pending join requests (§11 POST /groups/{gid}/members:approve).
// whatsmeow does not expose membership-approval in the surface wired for v1, so
// this is reported as not_implemented consistently with the media types.
func (s *GroupService) ApproveMembers(ctx context.Context, organizationID, sessionID, groupJID string, jids []string) error {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return err
	}
	return domain.ErrNotImplemented("group membership approval is not implemented yet")
}
