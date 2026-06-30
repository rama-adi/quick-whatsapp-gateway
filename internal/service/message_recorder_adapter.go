package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// MessageRecorderAdapter implements outbound.MessageRecorder over the MySQL chat
// and message repos. It writes the gateway's own successful sends into the
// messages table (from_me, direction=out, status=sent), mirroring the row the
// inbound pipeline stores for a received message, so a chat's history carries
// both sides of the conversation. The chat is upserted first so the row has a
// home and the chat's last_message_at advances; the message is then upserted
// keyed by (session_id, wa_message_id) — the same idempotent key inbound uses,
// so a later echo or receipt reconciles onto this row rather than duplicating.
type MessageRecorderAdapter struct {
	messages  *store.MessageRepo
	chats     *store.ChatRepo
	polls     *store.PollRepo
	scheduler pollRecapScheduler
	clock     func() int64
}

type pollRecapScheduler interface {
	Schedule(ctx context.Context, sessionID, pollMessageID string, endTimeMs int64)
}

// NewMessageRecorderAdapter wraps the chat + message repos for the
// outbound.Sender. clock may be nil (domain.NowMs is used).
func NewMessageRecorderAdapter(messages *store.MessageRepo, chats *store.ChatRepo, polls *store.PollRepo, scheduler pollRecapScheduler, clock func() int64) *MessageRecorderAdapter {
	if clock == nil {
		clock = domain.NowMs
	}
	return &MessageRecorderAdapter{messages: messages, chats: chats, polls: polls, scheduler: scheduler, clock: clock}
}

var _ outbound.MessageRecorder = (*MessageRecorderAdapter)(nil)

func (a *MessageRecorderAdapter) RecordSent(ctx context.Context, m outbound.SentMessage) error {
	now := a.clock()

	// Upsert the chat first so the message has a target and last_message_at
	// advances to this send (matches inbound persist ordering).
	if m.ChatJID != "" {
		if err := a.chats.Upsert(ctx, domain.Chat{
			SessionID:     m.SessionID,
			ChatJID:       m.ChatJID,
			Type:          chatTypeFromJID(m.ChatJID),
			LastMessageAt: int64Ptr(m.TimestampMs),
		}); err != nil {
			return err
		}
	}

	var mentions json.RawMessage
	if len(m.Mentions) > 0 {
		b, err := json.Marshal(m.Mentions)
		if err != nil {
			return err
		}
		mentions = b
	}

	if m.Type == domain.SendTypePoll && a.polls != nil {
		if err := a.polls.Upsert(ctx, domain.Poll{
			SessionID:       m.SessionID,
			PollMessageID:   m.WAMessageID,
			ChatJID:         m.ChatJID,
			Name:            m.Body,
			Options:         m.PollOptions,
			SelectableCount: m.PollSelectableCount,
			EndTime:         m.PollEndTime,
			HideVotes:       m.PollHideVotes,
			CreatedAt:       now,
			UpdatedAt:       now,
		}); err != nil {
			return err
		}
		if a.scheduler != nil {
			a.scheduler.Schedule(ctx, m.SessionID, m.WAMessageID, m.PollEndTime)
		}
	}

	status := domain.MessageSent
	return a.messages.Upsert(ctx, domain.Message{
		SessionID:       m.SessionID,
		WAMessageID:     m.WAMessageID,
		ChatJID:         m.ChatJID,
		FromMe:          true,
		Direction:       domain.DirectionOut,
		Type:            m.Type,
		Body:            stringPtr(m.Body),
		QuotedMessageID: stringPtr(m.ReplyTo),
		Mentions:        mentions,
		HasMedia:        m.HasMedia,
		MediaMeta:       m.MediaMeta,
		Status:          &status,
		Timestamp:       m.TimestampMs,
		CreatedAt:       now,
	})
}

// chatTypeFromJID classifies a recipient JID into a chats.type for an outbound
// send, by the WhatsApp server suffix. DMs (phone JID or @lid) are the default.
func chatTypeFromJID(jid string) domain.ChatType {
	switch {
	case strings.HasSuffix(jid, "@g.us"):
		return domain.ChatGroup
	case strings.HasSuffix(jid, "@newsletter"):
		return domain.ChatNewsletter
	case strings.HasSuffix(jid, "@broadcast"):
		return domain.ChatBroadcast
	default:
		return domain.ChatDM
	}
}
