package inbound

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// persist is stage 4 (§7.4): chat/message/poll-vote persistence and receipt
// status updates. It branches on the NormalizedMessage Kind:
//
//   - KindMessage  -> upsert chat + insert message row
//   - KindEdit     -> mark the target message edited (+ new body)
//   - KindRevoke   -> mark the target message deleted
//   - KindReceipt  -> update message status/ack_level for the acked ids
//   - KindPollVote -> insert a poll_votes row
//   - KindOther    -> nothing to persist (fan-out only)
func (p *Pipeline) persist(ctx context.Context, nm *NormalizedMessage) error {
	now := p.now()

	switch nm.Kind {
	case KindMessage:
		return p.persistMessage(ctx, nm, now)

	case KindEdit:
		if nm.WAMessageID == "" {
			return nil
		}
		if err := p.repos.MarkMessageEdited(ctx, nm.SessionID, nm.WAMessageID, nm.Body); err != nil {
			return fmt.Errorf("mark edited %q: %w", nm.WAMessageID, err)
		}
		return nil

	case KindRevoke:
		if nm.WAMessageID == "" {
			return nil
		}
		if err := p.repos.MarkMessageDeleted(ctx, nm.SessionID, nm.WAMessageID); err != nil {
			return fmt.Errorf("mark deleted %q: %w", nm.WAMessageID, err)
		}
		return nil

	case KindReceipt:
		return p.persistReceipt(ctx, nm, now)

	case KindPollVote:
		return p.persistPollVote(ctx, nm)

	default:
		// KindOther and anything else: nothing to persist.
		return nil
	}
}

func (p *Pipeline) persistMessage(ctx context.Context, nm *NormalizedMessage, now int64) error {
	// Upsert the chat first so the message FK target exists and last_message_at
	// advances to this message's timestamp.
	if nm.ChatJID != "" {
		if err := p.repos.UpsertChat(ctx, ChatUpsert{
			SessionID:     nm.SessionID,
			ChatJID:       nm.ChatJID,
			Type:          nm.ChatType,
			Name:          nm.ChatName,
			LastMessageAt: nm.TimestampMs,
			NowMs:         now,
		}); err != nil {
			return fmt.Errorf("upsert chat %q: %w", nm.ChatJID, err)
		}
	}

	dir := domain.DirectionIn
	if nm.FromMe {
		dir = domain.DirectionOut
	}

	// A poll-creation message also records its options so later votes (which
	// carry only option hashes) can be resolved to readable text.
	if nm.Poll != nil {
		if err := p.repos.UpsertPoll(ctx, PollUpsert{
			SessionID:       nm.SessionID,
			PollMessageID:   nm.WAMessageID,
			ChatJID:         nm.ChatJID,
			Name:            nm.Poll.Name,
			Options:         nm.Poll.Options,
			SelectableCount: nm.Poll.SelectableCount,
			NowMs:           now,
		}); err != nil {
			return fmt.Errorf("upsert poll %q: %w", nm.WAMessageID, err)
		}
	}

	if err := p.repos.InsertMessage(ctx, MessageInsert{
		SessionID:       nm.SessionID,
		WAMessageID:     nm.WAMessageID,
		ChatJID:         nm.ChatJID,
		SenderLID:       nm.SenderLID,
		SenderJID:       nm.SenderJID,
		FromMe:          nm.FromMe,
		Direction:       dir,
		Type:            nm.MsgType,
		Body:            nm.Body,
		QuotedMessageID: nm.QuotedMessageID,
		Mentions:        nm.Mentions,
		HasMedia:        nm.HasMedia,
		MediaMeta:       nm.MediaMeta,
		TimestampMs:     nm.TimestampMs,
		RawJSON:         nm.RawJSON,
		NowMs:           now,
	}); err != nil {
		return fmt.Errorf("insert message %q: %w", nm.WAMessageID, err)
	}
	return nil
}

func (p *Pipeline) persistReceipt(ctx context.Context, nm *NormalizedMessage, now int64) error {
	if nm.Receipt == nil || len(nm.Receipt.MessageIDs) == 0 {
		return nil
	}
	if err := p.repos.UpdateMessageStatus(ctx, MessageStatusUpdate{
		SessionID:    nm.SessionID,
		WAMessageIDs: nm.Receipt.MessageIDs,
		Status:       nm.Receipt.Status,
		AckLevel:     nm.Receipt.AckLevel,
		NowMs:        now,
	}); err != nil {
		return fmt.Errorf("update message status: %w", err)
	}
	return nil
}

func (p *Pipeline) persistPollVote(ctx context.Context, nm *NormalizedMessage) error {
	if nm.PollVote == nil {
		return nil
	}
	if err := p.repos.InsertPollVote(ctx, PollVoteInsert{
		SessionID:       nm.SessionID,
		PollMessageID:   nm.PollVote.PollMessageID,
		VoterLID:        nm.PollVote.VoterLID,
		SelectedOptions: nm.PollVote.SelectedOptions,
		TimestampMs:     nm.PollVote.TimestampMs,
		RawJSON:         nm.RawJSON,
	}); err != nil {
		return fmt.Errorf("insert poll vote %q: %w", nm.PollVote.PollMessageID, err)
	}
	return nil
}
