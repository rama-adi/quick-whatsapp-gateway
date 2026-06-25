package inbound

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// capture is stage 3 (§7.3): identity/contacts capture.
//
//   - Upsert the global identity (LID -> phone/name, push name preferred).
//   - Upsert the per-account contact for this session: DMs set seen_in_dm and the
//     DM first/last-seen timestamps; message_count is bumped only for real
//     messages (not receipts/presence). last_seen always advances via the upsert.
//   - For groups: upsert the group, then each participant as a member
//     (group_nickname + role).
//
// It runs for any kind that carries sender info; receipts/presence still refresh
// identity/contact "last seen" but do NOT bump message_count.
func (p *Pipeline) capture(ctx context.Context, nm *NormalizedMessage) error {
	now := p.now()

	// Identity + contact only make sense when we know who the sender is.
	if nm.SenderLID != "" {
		if err := p.repos.UpsertIdentity(ctx, IdentityUpsert{
			LID:          nm.SenderLID,
			PhoneNumber:  nm.SenderPhone,
			PhoneJID:     nm.SenderJID,
			Name:         nm.PushName,
			BusinessName: nm.BusinessName,
			NowMs:        now,
		}); err != nil {
			return fmt.Errorf("upsert identity %q: %w", nm.SenderLID, err)
		}

		// message_count bumps only for real, non-echo messages. Echoes
		// (FromMe) and receipts/poll-votes/presence don't count the peer as
		// "messaging us".
		bump := isContactBumpKind(nm.Kind) && !nm.FromMe
		if err := p.repos.UpsertContact(ctx, ContactUpsert{
			SessionID:        nm.SessionID,
			LID:              nm.SenderLID,
			SeenInDM:         nm.IsDM,
			BumpMessageCount: bump,
			NowMs:            now,
		}); err != nil {
			return fmt.Errorf("upsert contact %q: %w", nm.SenderLID, err)
		}
	}

	// Group + members capture for group chats.
	if nm.IsGroup && nm.Group != nil {
		if err := p.repos.UpsertGroup(ctx, GroupUpsert{
			GroupJID:         nm.Group.GroupJID,
			Subject:          nm.Group.Subject,
			Description:      nm.Group.Description,
			OwnerJID:         nm.Group.OwnerJID,
			ParticipantCount: nm.Group.ParticipantCount,
			IsAnnounce:       nm.Group.IsAnnounce,
			IsLocked:         nm.Group.IsLocked,
			CreatedAtWA:      nm.Group.CreatedAtWA,
			NowMs:            now,
		}); err != nil {
			return fmt.Errorf("upsert group %q: %w", nm.Group.GroupJID, err)
		}

		for _, m := range nm.Members {
			role := m.Role
			if role == "" {
				role = domain.RoleMember
			}
			if err := p.repos.UpsertGroupMember(ctx, GroupMemberUpsert{
				SessionID: nm.SessionID,
				GroupJID:  nm.Group.GroupJID,
				LID:       m.LID,
				Nickname:  m.Nickname,
				Role:      role,
				NowMs:     now,
			}); err != nil {
				return fmt.Errorf("upsert group member %q/%q: %w", nm.Group.GroupJID, m.LID, err)
			}
		}
	}

	return nil
}

// isContactBumpKind reports whether the event kind represents a real inbound
// message that should increment the contact's message_count.
func isContactBumpKind(k MessageKind) bool {
	switch k {
	case KindMessage:
		return true
	default:
		// edits/revokes mutate an existing message; receipts/poll-votes/other
		// don't represent a new message from the peer.
		return false
	}
}
