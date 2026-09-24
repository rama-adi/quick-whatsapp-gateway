package service

import (
	"context"
	"log/slog"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

// GroupService backs the group-management endpoints (§11 Groups). Reads
// (list/get/members) are served from the store; mutations and invite links go
// through the live GroupOps surface. When a GatewayGroupFacade is set (API
// composition), live calls execute through the private engine RPCs as durable
// commands, and this service persists its own projections from the raw results.
type GroupService struct {
	store *store.Store
	ops   GroupOps
	log   *slog.Logger
	// gatewayFacade is the control-plane group boundary (Increment 7). When set
	// it is preferred over the legacy in-process ops port.
	gatewayFacade GatewayGroupFacade
}

// NewGroupService constructs a GroupService. ops may be nil (live mutations then
// report the client as unavailable).
func NewGroupService(s *store.Store, ops GroupOps, log *slog.Logger) *GroupService {
	if log == nil {
		log = slog.Default()
	}
	return &GroupService{store: s, ops: ops, log: log}
}

// SetGatewayGroupFacade routes live group operations through the API-owned,
// resolved engine facade as durable commands. Gateway-local composition keeps
// its manager-backed ops until API composition supplies this seam.
func (s *GroupService) SetGatewayGroupFacade(facade GatewayGroupFacade) {
	if facade != nil {
		s.gatewayFacade = facade
	}
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

// liveFacade verifies ownership and returns the resolved facade, or nil when
// only the legacy in-process ops port is configured.
func (s *GroupService) liveFacade(ctx context.Context, organizationID, sessionID string) (GatewayGroupFacade, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return nil, err
	}
	if s.gatewayFacade != nil {
		return s.gatewayFacade, nil
	}
	if s.ops == nil {
		return nil, errLiveUnavailable()
	}
	return nil, nil
}

// Create creates a new group (§11 POST /groups). The engine returns the raw
// group metadata; the service upserts its own projection so both API-local and
// legacy paths leave the store equally populated.
func (s *GroupService) Create(
	ctx context.Context,
	organizationID, sessionID, name string,
	participants []string,
) (GroupInfo, error) {
	if name == "" {
		return GroupInfo{}, domain.ErrValidation("name is required")
	}
	facade, err := s.liveFacade(ctx, organizationID, sessionID)
	if err != nil {
		return GroupInfo{}, err
	}
	if facade == nil {
		return s.ops.CreateGroup(ctx, sessionID, name, participants)
	}
	info, err := facade.CreateGroup(ctx, organizationID, sessionID, name, participants)
	if err != nil {
		return GroupInfo{}, err
	}
	s.persistGroupProjection(ctx, sessionID, info)
	return info, nil
}

// persistGroupProjection best-effort upserts the stored group row from raw live
// metadata; a projection failure never fails the live operation itself.
func (s *GroupService) persistGroupProjection(ctx context.Context, sessionID string, info GroupInfo) {
	now := domain.NowMs()
	participants := info.Participants
	err := s.store.Groups.Upsert(ctx, domain.Group{
		GroupJID:         info.GroupJID,
		Subject:          stringPtr(info.Subject),
		Description:      stringPtr(info.Description),
		OwnerJID:         stringPtr(info.OwnerJID),
		ParticipantCount: &participants,
		IsAnnounce:       &info.IsAnnounce,
		IsLocked:         &info.IsLocked,
		FirstSeenAt:      now,
		UpdatedAt:        now,
	})
	if err != nil {
		s.log.WarnContext(ctx, "persist group projection", "group", info.GroupJID, "err", err)
		return
	}
	session, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil || session.WALID == nil || *session.WALID == "" {
		return
	}
	if err := s.store.GroupMembers.Upsert(ctx, domain.GroupMember{
		SessionID: sessionID, GroupJID: info.GroupJID, LID: *session.WALID,
		Role: domain.RoleSuperAdmin, FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		s.log.WarnContext(ctx, "persist creator group membership", "group", info.GroupJID, "err", err)
	}
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
func (s *GroupService) Members(
	ctx context.Context,
	organizationID, sessionID, groupJID string,
) ([]domain.GroupMember, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return nil, err
	}
	return s.store.GroupMembers.ListByGroup(ctx, sessionID, groupJID)
}

// participants applies an add/remove/promote/demote action.
func (s *GroupService) participants(
	ctx context.Context,
	organizationID, sessionID, groupJID string,
	jids []string,
	action GroupParticipantAction,
) error {
	if len(jids) == 0 {
		return domain.ErrValidation("at least one participant is required")
	}
	facade, err := s.liveFacade(ctx, organizationID, sessionID)
	if err != nil {
		return err
	}
	if facade == nil {
		return s.ops.UpdateParticipants(ctx, sessionID, groupJID, jids, action)
	}
	return facade.UpdateParticipants(ctx, organizationID, sessionID, groupJID, jids, action)
}

// AddMembers adds participants (§11 POST /groups/{gid}/members).
func (s *GroupService) AddMembers(
	ctx context.Context,
	organizationID, sessionID, groupJID string,
	jids []string,
) error {
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
func (s *GroupService) UpdateSettings(
	ctx context.Context,
	organizationID, sessionID, groupJID string,
	in GroupSettings,
) error {
	noSettings := in.Subject == nil && in.Description == nil && in.Announce == nil && in.Locked == nil
	if noSettings {
		return domain.ErrValidation("no group settings to update")
	}
	facade, err := s.liveFacade(ctx, organizationID, sessionID)
	if err != nil {
		return err
	}
	if facade == nil {
		return s.ops.UpdateSettings(ctx, sessionID, groupJID, in)
	}
	if err := facade.UpdateSettings(ctx, organizationID, sessionID, groupJID, in); err != nil {
		return err
	}
	s.refreshGroupProjection(ctx, groupJID)
	return nil
}

// refreshGroupProjection best-effort re-reads the stored row and bumps its
// timestamp after a settings mutation; the next backfill or inbound sync event
// carries the authoritative values.
func (s *GroupService) refreshGroupProjection(ctx context.Context, groupJID string) {
	group, err := s.store.Groups.GetByJID(ctx, groupJID)
	if err != nil {
		return
	}
	group.UpdatedAt = domain.NowMs()
	if err := s.store.Groups.Upsert(ctx, group); err != nil {
		s.log.WarnContext(ctx, "refresh group projection", "group", groupJID, "err", err)
	}
}

// InviteLink returns the group's invite link (§11 GET /groups/{gid}/invite).
func (s *GroupService) InviteLink(ctx context.Context, organizationID, sessionID, groupJID string) (string, error) {
	facade, err := s.liveFacade(ctx, organizationID, sessionID)
	if err != nil {
		return "", err
	}
	if facade == nil {
		return s.ops.GetInviteLink(ctx, sessionID, groupJID, false)
	}
	return facade.GetInviteLink(ctx, organizationID, sessionID, groupJID, false)
}

// RevokeInvite resets the invite link, returning the new one (§11 DELETE /groups/{gid}/invite).
func (s *GroupService) RevokeInvite(ctx context.Context, organizationID, sessionID, groupJID string) (string, error) {
	facade, err := s.liveFacade(ctx, organizationID, sessionID)
	if err != nil {
		return "", err
	}
	if facade == nil {
		return s.ops.GetInviteLink(ctx, sessionID, groupJID, true)
	}
	return facade.GetInviteLink(ctx, organizationID, sessionID, groupJID, true)
}

// Join joins a group from an invite code/link (§11 POST /groups:join).
func (s *GroupService) Join(ctx context.Context, organizationID, sessionID, invite string) (string, error) {
	if invite == "" {
		return "", domain.ErrValidation("invite is required")
	}
	facade, err := s.liveFacade(ctx, organizationID, sessionID)
	if err != nil {
		return "", err
	}
	if facade == nil {
		return s.ops.JoinWithLink(ctx, sessionID, invite)
	}
	return facade.JoinWithLink(ctx, organizationID, sessionID, invite)
}

// Leave leaves a group (§11 POST /groups/{gid}:leave).
func (s *GroupService) Leave(ctx context.Context, organizationID, sessionID, groupJID string) error {
	facade, err := s.liveFacade(ctx, organizationID, sessionID)
	if err != nil {
		return err
	}
	if facade == nil {
		return s.ops.Leave(ctx, sessionID, groupJID)
	}
	return facade.Leave(ctx, organizationID, sessionID, groupJID)
}

// ApproveMembers approves pending join requests (§11 POST /groups/{gid}/members:approve).
// whatsmeow does not expose membership-approval in the surface wired for v1, so
// this is reported as not_implemented consistently with the media types.
func (s *GroupService) ApproveMembers(
	ctx context.Context,
	organizationID, sessionID, groupJID string,
	jids []string,
) error {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return err
	}
	return domain.ErrNotImplemented("group membership approval is not implemented yet")
}
