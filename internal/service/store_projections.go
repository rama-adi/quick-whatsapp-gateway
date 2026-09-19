package service

import (
	"context"
	"encoding/json"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

// StoreProjections adapts the concrete WhatsApp-data repositories to the
// EventProjectionConsumer's port. It preserves the pipeline's session/org tags
// and natural upsert keys so protocol redelivery updates rather than duplicates.
type StoreProjections struct {
	st *store.Store
}

// NewStoreProjections wires the projection consumer's store port.
func NewStoreProjections(st *store.Store) *StoreProjections {
	return &StoreProjections{st: st}
}

var _ projectionStore = (*StoreProjections)(nil)

func (p *StoreProjections) UpsertIdentity(ctx context.Context, in ProjectionIdentityUpsert) error {
	return p.st.Identities.Upsert(ctx, domain.Identity{
		LID:          in.LID,
		PhoneNumber:  stringPtr(in.PhoneNumber),
		PhoneJID:     stringPtr(in.PhoneJID),
		Name:         stringPtr(in.Name),
		BusinessName: stringPtr(in.BusinessName),
		FirstSeenAt:  in.NowMs,
		UpdatedAt:    in.NowMs,
	})
}

func (p *StoreProjections) FillIdentityName(ctx context.Context, jid, name string, nowMs int64) error {
	return p.st.Identities.FillNameByJID(ctx, jid, name, nowMs)
}

// AttachPairing routes a PairSuccess projection onto the session repo.
func (p *StoreProjections) AttachPairing(ctx context.Context, in store.AttachPairingInput) error {
	return p.st.Sessions.AttachPairing(ctx, in)
}

// ClearPairing routes a logged-out projection onto the session repo.
func (p *StoreProjections) ClearPairing(ctx context.Context, sessionID string, updatedAt int64) error {
	return p.st.Sessions.ClearPairing(ctx, sessionID, updatedAt)
}

func (p *StoreProjections) UpsertGroup(ctx context.Context, in ProjectionGroupUpsert) error {
	return p.st.Groups.Upsert(ctx, domain.Group{
		GroupJID:         in.GroupJID,
		Subject:          stringPtr(in.Subject),
		Description:      stringPtr(in.Description),
		OwnerJID:         stringPtr(in.OwnerJID),
		ParticipantCount: in.ParticipantCount,
		IsAnnounce:       in.IsAnnounce,
		IsLocked:         in.IsLocked,
		CreatedAtWA:      in.CreatedAtWA,
		FirstSeenAt:      in.NowMs,
		UpdatedAt:        in.NowMs,
	})
}

func (p *StoreProjections) UpsertGroupMember(ctx context.Context, in ProjectionGroupMemberUpsert) error {
	return p.st.GroupMembers.Upsert(ctx, domain.GroupMember{
		SessionID:   in.SessionID,
		GroupJID:    in.GroupJID,
		LID:         in.LID,
		Tag:         stringPtr(in.Tag),
		Role:        in.Role,
		FirstSeenAt: in.NowMs,
		LastSeenAt:  in.NowMs,
	})
}

func (p *StoreProjections) UpsertChat(ctx context.Context, in ProjectionChatUpsert) error {
	return p.st.Chats.Upsert(ctx, domain.Chat{
		SessionID:     in.SessionID,
		ChatJID:       in.ChatJID,
		Type:          in.Type,
		Name:          stringPtr(in.Name),
		LastMessageAt: int64Ptr(in.LastMessageAt),
	})
}

func (p *StoreProjections) InsertMessage(ctx context.Context, in ProjectionMessageInsert) error {
	var mentions json.RawMessage
	if len(in.Mentions) > 0 {
		b, err := json.Marshal(in.Mentions)
		if err != nil {
			return err
		}
		mentions = b
	}
	return p.st.Messages.Upsert(ctx, domain.Message{
		SessionID:       in.SessionID,
		WAMessageID:     in.WAMessageID,
		ChatJID:         in.ChatJID,
		SenderLID:       stringPtr(in.SenderLID),
		SenderJID:       stringPtr(in.SenderJID),
		FromMe:          in.FromMe,
		Direction:       in.Direction,
		Type:            in.Type,
		Body:            stringPtr(in.Body),
		QuotedMessageID: stringPtr(in.QuotedMessageID),
		Mentions:        mentions,
		HasMedia:        in.HasMedia,
		MediaMeta:       in.MediaMeta,
		Timestamp:       in.TimestampMs,
		RawJSON:         json.RawMessage(in.RawJSON),
		CreatedAt:       in.NowMs,
	})
}

func (p *StoreProjections) MarkMessageEdited(ctx context.Context, sessionID, waMessageID, newBody string) error {
	return p.st.Messages.MarkEdited(ctx, sessionID, waMessageID, newBody)
}

func (p *StoreProjections) MarkMessageDeleted(ctx context.Context, sessionID, waMessageID string) error {
	return p.st.Messages.MarkDeleted(ctx, sessionID, waMessageID)
}

func (p *StoreProjections) UpdateMessageStatus(ctx context.Context, in ProjectionMessageStatusUpdate) error {
	for _, id := range in.WAMessageIDs {
		if err := p.st.Messages.AdvanceReceiptStatus(ctx, in.SessionID, id, in.Status, in.AckLevel); err != nil {
			return err
		}
	}
	return nil
}

func (p *StoreProjections) UpsertPoll(ctx context.Context, in ProjectionPollUpsert) error {
	return p.st.Polls.Upsert(ctx, domain.Poll{
		SessionID:       in.SessionID,
		PollMessageID:   in.PollMessageID,
		ChatJID:         in.ChatJID,
		Name:            in.Name,
		Options:         in.Options,
		SelectableCount: in.SelectableCount,
		EndTime:         in.EndTime,
		HideVotes:       in.HideVotes,
		CreatedAt:       in.NowMs,
		UpdatedAt:       in.NowMs,
	})
}

func (p *StoreProjections) InsertPollVote(ctx context.Context, in ProjectionPollVoteInsert) error {
	_, err := p.st.PollVotes.Insert(ctx, domain.PollVote{
		SessionID:       in.SessionID,
		PollMessageID:   in.PollMessageID,
		VoterLID:        in.VoterLID,
		SelectedOptions: json.RawMessage(in.SelectedOptions),
		Timestamp:       in.TimestampMs,
		RawJSON:         json.RawMessage(in.RawJSON),
	})
	return err
}
