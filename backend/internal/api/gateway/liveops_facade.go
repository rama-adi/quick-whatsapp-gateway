// Live-operations facades adapt the private engine client onto the service
// layer's resolved live-operation ports. The EngineClient owns session→gateway
// resolution, connection pooling, deadlines, and transport error mapping; these
// adapters translate the service package's org/session signatures onto it so
// REST services execute their live calls API-locally (Increment 7).
package gateway

import (
	"context"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// LiveOpsFacade implements every Increment 7 resource facade over one
// EngineClient. The client's methods already resolve targets and map errors;
// this type only re-shapes signatures.
type LiveOpsFacade struct {
	client *EngineClient
}

// NewLiveOpsFacade wraps an engine client for the resource services.
func NewLiveOpsFacade(client *EngineClient) *LiveOpsFacade {
	return &LiveOpsFacade{client: client}
}

// LookupContact checks phone numbers on WhatsApp through the assigned engine.
func (f *LiveOpsFacade) LookupContact(
	ctx context.Context,
	organizationID string,
	sessionID string,
	phones []string,
) ([]domain.OnWhatsApp, error) {
	results, err := f.client.LookupContact(ctx, organizationID, sessionID, phones)
	if err != nil {
		return nil, err
	}
	out := make([]domain.OnWhatsApp, 0, len(results))
	for _, r := range results {
		out = append(out, domain.OnWhatsApp{Query: r.Query, JID: r.JID, IsIn: r.IsIn})
	}
	return out, nil
}

// GetContactPicture fetches a contact's profile picture.
func (f *LiveOpsFacade) GetContactPicture(
	ctx context.Context,
	organizationID string,
	sessionID string,
	jid string,
) (domain.ProfilePicture, error) {
	return f.client.GetContactPicture(ctx, organizationID, sessionID, jid)
}

// GetContactAbout fetches a contact's status text.
func (f *LiveOpsFacade) GetContactAbout(ctx context.Context, organizationID, sessionID, jid string) (string, error) {
	return f.client.GetContactAbout(ctx, organizationID, sessionID, jid)
}

// SetBlocked applies one block/unblock command through the assigned engine.
func (f *LiveOpsFacade) SetBlocked(ctx context.Context, organizationID, sessionID, jid string, blocked bool) error {
	return f.client.SetBlocked(ctx, organizationID, sessionID, jid, blocked)
}

// CreateGroup creates a group and returns its raw live metadata. Projection
// persistence stays with the calling service.
func (f *LiveOpsFacade) CreateGroup(
	ctx context.Context,
	organizationID string,
	sessionID string,
	name string,
	participants []string,
) (domain.GroupInfo, error) {
	result, err := f.client.MutateGroup(ctx, application.GroupMutationCommand{
		CommandID:      domain.NewULID(),
		OrganizationID: organizationID,
		SessionID:      sessionID,
		Kind:           application.GroupOpCreate,
		Name:           name,
		Participants:   participants,
	})
	if err != nil {
		return domain.GroupInfo{}, err
	}
	return groupInfoFromResult(result.CreatedGroup), nil
}

// UpdateParticipants applies one add/remove/promote/demote as a durable command.
func (f *LiveOpsFacade) UpdateParticipants(
	ctx context.Context,
	organizationID string,
	sessionID string,
	groupJID string,
	participants []string,
	action domain.GroupParticipantAction,
) error {
	_, err := f.client.MutateGroup(ctx, application.GroupMutationCommand{
		CommandID:      domain.NewULID(),
		OrganizationID: organizationID,
		SessionID:      sessionID,
		Kind:           application.GroupOpUpdateParticipants,
		GroupJID:       groupJID,
		Participants:   participants,
		Action:         application.GroupParticipantChange(action),
	})
	return err
}

// UpdateSettings applies subject/description/announce/locked as a durable
// command; nil fields are unchanged.
func (f *LiveOpsFacade) UpdateSettings(
	ctx context.Context,
	organizationID string,
	sessionID string,
	groupJID string,
	s domain.GroupSettings,
) error {
	_, err := f.client.MutateGroup(ctx, application.GroupMutationCommand{
		CommandID:      domain.NewULID(),
		OrganizationID: organizationID,
		SessionID:      sessionID,
		Kind:           application.GroupOpUpdateSettings,
		GroupJID:       groupJID,
		Settings: application.GroupSettingsUpdate{
			Subject:     s.Subject,
			Description: s.Description,
			Announce:    s.Announce,
			Locked:      s.Locked,
		},
	})
	return err
}

// GetInviteLink reads (reset=false) or resets (reset=true) a group invite link.
func (f *LiveOpsFacade) GetInviteLink(
	ctx context.Context,
	organizationID string,
	sessionID string,
	groupJID string,
	reset bool,
) (string, error) {
	return f.client.GetGroupInviteLink(ctx, organizationID, sessionID, groupJID, reset)
}

// JoinWithLink joins a group from an invite code/link.
func (f *LiveOpsFacade) JoinWithLink(ctx context.Context, organizationID, sessionID, code string) (string, error) {
	return f.client.JoinGroup(ctx, organizationID, sessionID, code)
}

// Leave leaves a group as a durable command.
func (f *LiveOpsFacade) Leave(ctx context.Context, organizationID, sessionID, groupJID string) error {
	_, err := f.client.MutateGroup(ctx, application.GroupMutationCommand{
		CommandID:      domain.NewULID(),
		OrganizationID: organizationID, SessionID: sessionID,
		Kind: application.GroupOpLeave, GroupJID: groupJID,
	})
	return err
}

// GetChatPresence subscribes to a contact's presence and returns the snapshot.
func (f *LiveOpsFacade) GetChatPresence(
	ctx context.Context,
	organizationID string,
	sessionID string,
	chatJID string,
) (domain.PresenceStatus, error) {
	return f.client.GetChatPresence(ctx, organizationID, sessionID, chatJID)
}

// SetChatPresence sends per-chat typing state through the assigned engine.
func (f *LiveOpsFacade) SetChatPresence(ctx context.Context, organizationID, sessionID, chatJID, state string) error {
	return f.client.SetChatPresence(ctx, organizationID, sessionID, chatJID, state)
}

// BackfillSessionData pulls the raw backfill snapshot from the assigned engine
// under the send deadline. Projection persistence stays with the caller.
func (f *LiveOpsFacade) BackfillSessionData(
	ctx context.Context,
	organizationID string,
	sessionID string,
) (domain.BackfillSnapshot, error) {
	return f.client.BackfillSession(ctx, organizationID, sessionID)
}

func groupInfoFromResult(info application.GroupInfoResult) domain.GroupInfo {
	return domain.GroupInfo{
		GroupJID: info.GroupJID, Subject: info.Subject, Description: info.Description,
		OwnerJID: info.OwnerJID, Participants: int(info.Participants),
		IsAnnounce: info.IsAnnounce, IsLocked: info.IsLocked,
	}
}
