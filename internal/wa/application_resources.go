package wa

import (
	"context"
	"errors"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// Live resource operations use the shared command executor for durable mutations.
//
// Reads (contact lookups, picture/about, invite links, chat-presence
// subscription, backfill) execute directly behind the assignment fence and
// do not use the durable result ledger.
// Mutations (block/unblock, group create/settings/participants/leave) reuse the
// send pipeline's shared command executor so a repeated
// command_id returns the stored terminal outcome instead of re-executing.

func (a *ApplicationGatewayAdapter) fenceQuery(query application.SessionStateQuery) error {
	if err := a.validateTarget(
		query.OrganizationID,
		query.SessionID,
		query.GatewayID,
	); err != nil {
		return err
	}
	if a.fence != nil {
		ownsAssignment := query.AssignmentEpoch != 0 &&
			a.fence.OwnsSession(query.OrganizationID, query.SessionID, query.AssignmentEpoch)
		if !ownsAssignment {
			return domain.ErrConflict("session assignment is not live")
		}
	}
	return nil
}

// LookupContact checks phone numbers against WhatsApp. Read-only: no ledger.
func (a *ApplicationGatewayAdapter) LookupContact(
	ctx context.Context,
	command application.LookupContactCommand,
) ([]application.ContactLookup, error) {
	query := sessionQueryFrom(
		command.OrganizationID,
		command.SessionID,
		command.GatewayID,
		command.AssignmentEpoch,
	)
	if err := a.fenceQuery(query); err != nil {
		return nil, err
	}
	if len(command.Phones) == 0 {
		return nil, domain.ErrValidation("phones must not be empty")
	}
	responses, err := a.live.IsOnWhatsApp(ctx, command.SessionID, command.Phones)
	if err != nil {
		return nil, err
	}
	out := make([]application.ContactLookup, 0, len(responses))
	for _, r := range responses {
		out = append(out, application.ContactLookup{Query: r.Query, JID: r.JID, IsIn: r.IsIn})
	}
	return out, nil
}

// GetContactPicture fetches a contact's profile picture. Read-only: no ledger.
func (a *ApplicationGatewayAdapter) GetContactPicture(
	ctx context.Context,
	query application.SessionStateQuery,
	jid string,
) (domain.ProfilePicture, error) {
	if err := a.fenceQuery(query); err != nil {
		return domain.ProfilePicture{}, err
	}
	if jid == "" {
		return domain.ProfilePicture{}, domain.ErrValidation("jid is required")
	}
	return a.live.ProfilePicture(ctx, query.SessionID, jid)
}

// GetContactAbout fetches a contact's status text. Read-only: no ledger.
func (a *ApplicationGatewayAdapter) GetContactAbout(
	ctx context.Context,
	query application.SessionStateQuery,
	jid string,
) (string, error) {
	if err := a.fenceQuery(query); err != nil {
		return "", err
	}
	if jid == "" {
		return "", domain.ErrValidation("jid is required")
	}
	return a.live.About(ctx, query.SessionID, jid)
}

// SetBlocked applies one block/unblock command behind the assignment fence with
// full ledger semantics. Only CommandSent is recorded — the blocklist mutation
// carries no WhatsApp message id — and stored failures replay as validation.
func (a *ApplicationGatewayAdapter) SetBlocked(
	ctx context.Context,
	command application.ContactJIDCommand,
) (application.MutationOnlyResult, error) {
	return runDurableCommand(ctx, a, durableCommand[application.MutationOnlyResult]{
		Operation: fmt.Sprintf("blocked:%t", command.Blocked),
		Target: mutationResult(
			command.CommandID,
			command.OrganizationID,
			command.SessionID,
			command.GatewayID,
			command.AssignmentEpoch,
		),
		Replay: func(record *application.CommandResultRecord) (application.MutationOnlyResult, error) {
			return replayedBlockingMutation(record)
		},
		Execute: func() (application.MutationOnlyResult, error) {
			err := a.live.SetBlocked(ctx, command.SessionID, command.JID, command.Blocked)
			if err != nil {
				return application.MutationOnlyResult{}, err
			}
			result := application.MutationOnlyResult{
				MutationResult: mutationResult(
					command.CommandID,
					command.OrganizationID,
					command.SessionID,
					command.GatewayID,
					command.AssignmentEpoch,
				),
			}
			if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
				CommandID: command.CommandID, SessionID: command.SessionID,
				Status: application.CommandSent, UpdatedAt: a.now().UTC(),
			}); saveErr != nil {
				return application.MutationOnlyResult{}, saveErr
			}
			return result, nil
		},
	})
}

// replayedBlockingMutation reconstructs a stored blocklist outcome. Successes
// carry routing metadata only; failures replay as validation errors.
func replayedBlockingMutation(record *application.CommandResultRecord) (application.MutationOnlyResult, error) {
	switch record.Status {
	case application.CommandSent:
		return application.MutationOnlyResult{MutationResult: application.MutationResult{
			CommandID: record.CommandID,
		}}, nil
	case application.CommandFailed:
		return application.MutationOnlyResult{}, domain.ErrValidation("op previously failed: " + record.Error)
	default:
		return application.MutationOnlyResult{}, fmt.Errorf("unknown stored command status %q", record.Status)
	}
}

// MutateGroup executes one durable group command behind the assignment fence
// with the same ledger semantics as SendMessage. The returned result carries
// raw live metadata; the API persists its own projections from it.
func (a *ApplicationGatewayAdapter) MutateGroup(
	ctx context.Context,
	command application.GroupMutationCommand,
) (application.GroupCreateResult, error) {
	return runDurableCommand(ctx, a, durableCommand[application.GroupCreateResult]{
		Operation: "group:" + string(command.Kind),
		Target: mutationResult(
			command.CommandID,
			command.OrganizationID,
			command.SessionID,
			command.GatewayID,
			command.AssignmentEpoch,
		),
		Replay: func(record *application.CommandResultRecord) (application.GroupCreateResult, error) {
			return replayedGroupMutation(command, record)
		},
		Execute: func() (application.GroupCreateResult, error) {
			info, groupErr := a.executeGroupMutation(ctx, command)
			if groupErr != nil {
				// Deterministic rejections are recorded as terminal failures; everything
				// else stays unrecorded for retry/reconciliation.
				var apiErr *domain.APIError
				if errors.As(groupErr, &apiErr) && apiErr.Code == domain.CodeValidationError {
					if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
						CommandID: command.CommandID, SessionID: command.SessionID,
						Status: application.CommandFailed, Error: groupErr.Error(), UpdatedAt: a.now().UTC(),
					}); saveErr != nil {
						return application.GroupCreateResult{}, saveErr
					}
				}
				return application.GroupCreateResult{}, groupErr
			}
			sentAt := a.now().UTC()
			result := application.GroupCreateResult{
				MutationOnlyResult: application.MutationOnlyResult{
					MutationResult: mutationResult(
						command.CommandID,
						command.OrganizationID,
						command.SessionID,
						command.GatewayID,
						command.AssignmentEpoch,
					),
				},
				CreatedGroup: groupInfoResult(info),
			}
			if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
				CommandID: command.CommandID, SessionID: command.SessionID,
				Status: application.CommandSent, WAMessageID: info.GroupJID, UpdatedAt: sentAt,
			}); saveErr != nil {
				return application.GroupCreateResult{}, saveErr
			}
			return result, nil
		},
	})
}

// executeGroupMutation routes one validated group command to the live client.
// JID/action validation happens here so deterministic rejections can be
// recorded as terminal failures.
func (a *ApplicationGatewayAdapter) executeGroupMutation(
	ctx context.Context,
	command application.GroupMutationCommand,
) (domain.GroupInfo, error) {
	switch command.Kind {
	case application.GroupOpCreate:
		if len(command.Participants) == 0 {
			return domain.GroupInfo{}, domain.ErrValidation("at least one participant is required")
		}
		return a.live.CreateGroup(
			ctx,
			command.SessionID,
			command.Name,
			command.Participants,
		)
	case application.GroupOpUpdateSettings:
		noSettings := command.Settings.Subject == nil &&
			command.Settings.Description == nil &&
			command.Settings.Announce == nil &&
			command.Settings.Locked == nil
		if noSettings {
			return domain.GroupInfo{}, domain.ErrValidation("no group settings to update")
		}
		return domain.GroupInfo{}, a.live.UpdateSettings(
			ctx,
			command.SessionID,
			command.GroupJID,
			application.ToDomainGroupSettings(command.Settings),
		)
	case application.GroupOpUpdateParticipants:
		if len(command.Participants) == 0 {
			return domain.GroupInfo{}, domain.ErrValidation("at least one participant is required")
		}
		action := domain.GroupParticipantAction(command.Action)
		switch action {
		case domain.GroupActionAdd, domain.GroupActionRemove, domain.GroupActionPromote, domain.GroupActionDemote:
		default:
			return domain.GroupInfo{}, domain.ErrValidation("invalid participant action")
		}
		return domain.GroupInfo{}, a.live.UpdateParticipants(
			ctx,
			command.SessionID,
			command.GroupJID,
			command.Participants,
			action,
		)
	case application.GroupOpLeave:
		return domain.GroupInfo{}, a.live.Leave(ctx, command.SessionID, command.GroupJID)
	default:
		return domain.GroupInfo{}, domain.ErrValidation("invalid group operation")
	}
}

// replayedGroupMutation reconstructs a stored group-command outcome. Create
// replays carry the original group's live metadata; other kinds return routing
// metadata only. Failures replay as validation errors.
func replayedGroupMutation(
	command application.GroupMutationCommand,
	record *application.CommandResultRecord,
) (application.GroupCreateResult, error) {
	meta := mutationResult(
		record.CommandID,
		command.OrganizationID,
		command.SessionID,
		command.GatewayID,
		command.AssignmentEpoch,
	)
	switch record.Status {
	case application.CommandSent:
		result := application.GroupCreateResult{
			MutationOnlyResult: application.MutationOnlyResult{MutationResult: meta},
		}
		if command.Kind == application.GroupOpCreate {
			result.CreatedGroup = application.GroupInfoResult{GroupJID: record.WAMessageID}
		}
		return result, nil
	case application.CommandFailed:
		return application.GroupCreateResult{}, domain.ErrValidation("group op previously failed: " + record.Error)
	default:
		return application.GroupCreateResult{}, fmt.Errorf("unknown stored command status %q", record.Status)
	}
}

// GetGroupInviteLink reads (reset=false) or revokes-and-regenerates (reset=true)
// a group's invite link. Not ledger-backed, mirroring the LiveOps surface.
func (a *ApplicationGatewayAdapter) GetGroupInviteLink(
	ctx context.Context,
	query application.SessionStateQuery,
	groupJID string,
	reset bool,
) (string, error) {
	if err := a.fenceQuery(query); err != nil {
		return "", err
	}
	if groupJID == "" {
		return "", domain.ErrValidation("group_jid is required")
	}
	return a.live.GetInviteLink(ctx, query.SessionID, groupJID, reset)
}

// JoinGroup joins a group from an invite code/link. Read-classified like its
// LiveOps counterpart: no ledger entry.
func (a *ApplicationGatewayAdapter) JoinGroup(
	ctx context.Context,
	query application.SessionStateQuery,
	invite string,
) (string, error) {
	if err := a.fenceQuery(query); err != nil {
		return "", err
	}
	if invite == "" {
		return "", domain.ErrValidation("invite is required")
	}
	return a.live.JoinWithLink(ctx, query.SessionID, invite)
}

// GetChatPresence subscribes to a contact's presence updates and returns the
// unknown snapshot. Read-only: no ledger.
func (a *ApplicationGatewayAdapter) GetChatPresence(
	ctx context.Context,
	query application.SessionStateQuery,
	chatJID string,
) (domain.PresenceStatus, error) {
	if err := a.fenceQuery(query); err != nil {
		return domain.PresenceStatus{}, err
	}
	if chatJID == "" {
		return domain.PresenceStatus{}, domain.ErrValidation("chat_jid is required")
	}
	return a.live.GetPresence(ctx, query.SessionID, chatJID)
}

// SetChatPresence sends per-chat typing state. Not ledger-backed: repeating a
// typing state is idempotent by construction and the legacy port never deduped.
func (a *ApplicationGatewayAdapter) SetChatPresence(
	ctx context.Context,
	command application.ChatPresenceCommand,
) error {
	query := sessionQueryFrom(
		command.OrganizationID,
		command.SessionID,
		command.GatewayID,
		command.AssignmentEpoch,
	)
	if err := a.fenceQuery(query); err != nil {
		return err
	}
	switch command.State {
	case "composing", "paused", "recording":
	default:
		return domain.ErrValidation("invalid chat presence state")
	}
	return a.live.SetChatPresence(ctx, command.SessionID, command.ChatJID, command.State)
}

// BackfillSession pulls the session's direct-API snapshot. Slow read: no
// ledger, callers apply the send deadline.
func (a *ApplicationGatewayAdapter) BackfillSession(
	ctx context.Context,
	query application.SessionStateQuery,
) (domain.BackfillSnapshot, error) {
	if err := a.fenceQuery(query); err != nil {
		return domain.BackfillSnapshot{}, err
	}
	return a.live.BackfillSessionData(ctx, query.SessionID)
}
