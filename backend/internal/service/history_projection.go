package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

// History sync projection. history.batch events carry imported chat history;
// they never go through the live message projection (no OAuth interception,
// no message webhooks, no media capture). The store applies each batch in one
// transaction behind an event-id ledger.

const dayMs = int64(24 * 60 * 60 * 1000)

// WithRetentionDays sets the deployment's RETENTION_DAYS so imported history
// older than the retention window is not stored only to be pruned again. Zero
// or negative keeps everything.
func (c *EventProjectionConsumer) WithRetentionDays(days int) *EventProjectionConsumer {
	if c != nil {
		c.retentionDays = days
	}
	return c
}

func (c *EventProjectionConsumer) retentionCutoff(now int64) int64 {
	if c.retentionDays <= 0 {
		return 0
	}
	return now - int64(c.retentionDays)*dayMs
}

// projectHistoryBatch stores one history.batch. A payload that cannot be
// decoded or lacks its ids is logged and skipped: the committed-event worker
// processes a session strictly in order, so returning an error for a bad
// payload would block every later event of the session forever.
func (c *EventProjectionConsumer) projectHistoryBatch(ctx context.Context, event domain.Event) error {
	var p apitypes.HistoryBatchPayload
	if err := decodePayload(event, &p); err != nil {
		slog.WarnContext(ctx, "history batch payload undecodable; skipped",
			"event_id", event.ID, "session_id", event.Session, "err", err)
		return nil
	}
	if p.RunID == "" {
		slog.WarnContext(ctx, "history batch without run id; skipped",
			"event_id", event.ID, "session_id", event.Session)
		return nil
	}
	now := c.clock()
	batch, err := historyBatchFromPayload(event, p, now, c.retentionCutoff(now))
	if err != nil {
		slog.WarnContext(ctx, "history batch payload invalid; skipped",
			"event_id", event.ID, "session_id", event.Session, "err", err)
		return nil
	}
	res, err := c.store.ApplyHistoryBatch(ctx, batch)
	if err != nil {
		return err
	}
	slog.DebugContext(ctx, "history batch projected",
		"event_id", event.ID, "session_id", event.Session, "run_id", p.RunID,
		"applied", res.Applied, "imported", res.Imported, "existing", res.Existing, "dropped", res.Dropped)
	return nil
}

// projectHistorySync records a history.sync lifecycle event on its run.
func (c *EventProjectionConsumer) projectHistorySync(ctx context.Context, event domain.Event) error {
	var p apitypes.HistorySyncPayload
	if err := decodePayload(event, &p); err != nil {
		slog.WarnContext(ctx, "history sync payload undecodable; skipped",
			"event_id", event.ID, "session_id", event.Session, "err", err)
		return nil
	}
	switch p.Phase {
	case domain.HistoryPhaseStarted, domain.HistoryPhaseProgress, domain.HistoryPhaseCompleted, domain.HistoryPhaseFailed:
	default:
		slog.WarnContext(ctx, "history sync with unknown phase; skipped",
			"event_id", event.ID, "session_id", event.Session, "phase", p.Phase)
		return nil
	}
	if p.RunID == "" {
		slog.WarnContext(ctx, "history sync without run id; skipped",
			"event_id", event.ID, "session_id", event.Session)
		return nil
	}
	return c.store.ApplyHistorySync(ctx, store.HistorySyncUpdate{
		SessionID:     event.Session,
		RunID:         p.RunID,
		Phase:         p.Phase,
		AccountJID:    p.AccountJID,
		SyncType:      p.SyncType,
		Progress:      p.Progress,
		CutoffAt:      p.CutoffAt,
		Chunks:        p.Chunks,
		Batches:       p.Batches,
		Conversations: p.Conversations,
		Messages:      p.Messages,
		Skipped:       p.Skipped,
		Error:         p.Error,
		Now:           c.clock(),
	})
}

// historyBatchFromPayload flattens the wire batch into the store input. Chat
// types are derived from the JID (as for live messages), not trusted from the
// payload; message rows reuse the live projection's body/raw_json rules.
func historyBatchFromPayload(event domain.Event, p apitypes.HistoryBatchPayload, now, cutoff int64) (store.HistoryBatch, error) {
	b := store.HistoryBatch{
		EventID:         event.ID,
		SessionID:       event.Session,
		RunID:           p.RunID,
		AccountJID:      p.AccountJID,
		SnapshotAt:      p.SnapshotAt,
		RetentionCutoff: cutoff,
		Now:             now,
		Contacts:        make([]store.HistoryContact, 0, len(p.Contacts)),
		Conversations:   make([]store.HistoryConversation, 0, len(p.Conversations)),
	}
	if b.SnapshotAt <= 0 {
		b.SnapshotAt = event.Timestamp
	}
	for _, ct := range p.Contacts {
		b.Contacts = append(b.Contacts, store.HistoryContact{LID: ct.LID, PhoneJID: ct.PhoneJID, PushName: ct.PushName})
	}
	for _, conv := range p.Conversations {
		if conv.ChatJID == "" {
			continue
		}
		out := store.HistoryConversation{
			ChatJID:     conv.ChatJID,
			Type:        chatTypeFromJID(conv.ChatJID),
			Name:        conv.Name,
			Description: conv.Description,
			LID:         conv.LID,
			PhoneJID:    conv.PhoneJID,
			UnreadCount: conv.UnreadCount,
			Archived:    conv.Archived,
			Pinned:      conv.Pinned,
			Messages:    make([]store.HistoryMessage, 0, len(conv.Messages)),
		}
		if conv.MutedUntil > 0 {
			muted := conv.MutedUntil
			out.MutedUntil = &muted
		}
		for _, part := range conv.Participants {
			out.Participants = append(out.Participants, store.HistoryParticipant{
				LID: part.LID, PhoneJID: part.PhoneJID, Role: domain.GroupRole(part.Role),
			})
		}
		for _, m := range conv.Messages {
			hm, err := historyMessageFromPayload(conv.ChatJID, m)
			if err != nil {
				return store.HistoryBatch{}, err
			}
			out.Messages = append(out.Messages, hm)
		}
		b.Conversations = append(b.Conversations, out)
	}
	return b, nil
}

func historyMessageFromPayload(chatJID string, m apitypes.HistoryMessage) (store.HistoryMessage, error) {
	p := m.MessagePayload
	raw, err := json.Marshal(p)
	if err != nil {
		return store.HistoryMessage{}, err
	}
	kind := store.HistoryMessageKind(m.Kind)
	if kind == "" {
		kind = store.HistoryKindMessage
		if p.FromMe {
			kind = store.HistoryKindFromMe
		}
	}
	body := p.Body
	if kind != store.HistoryKindEdited && body == "" && p.Location == nil && p.Contact == nil && p.Poll == nil {
		// Same rule as the live projection: a frame with no displayable text
		// keeps its normalized payload JSON as the row body.
		body = string(raw)
	}
	out := store.HistoryMessage{
		Kind:            kind,
		WAMessageID:     p.WAMessageID,
		SenderLID:       p.SenderLID,
		SenderJID:       p.SenderJID,
		PushName:        p.PushName,
		FromMe:          p.FromMe,
		Type:            p.Type,
		Body:            body,
		QuotedMessageID: p.QuotedMessageID,
		Mentions:        mentionKeys(p.Mentions),
		HasMedia:        p.HasMedia,
		MediaMeta:       historyMediaMeta(p.Media),
		Timestamp:       p.Timestamp,
		RawJSON:         raw,
		TargetID:        p.TargetID,
	}
	if out.Type == "" {
		out.Type = "system"
	}
	if strings.HasSuffix(chatJID, "@g.us") && out.SenderLID == "" && strings.HasSuffix(out.SenderJID, "@lid") {
		out.SenderLID, out.SenderJID = out.SenderJID, ""
	}
	if s := domain.MessageStatus(m.Status); m.Status != "" && validMessageStatus(s) {
		out.Status = &s
	}
	if p.Poll != nil {
		out.Poll = &store.HistoryPoll{
			Name:            p.Poll.Name,
			Options:         p.Poll.Options,
			SelectableCount: p.Poll.SelectableCount,
			EndTime:         p.Poll.EndTime,
			HideVotes:       p.Poll.HideVotes,
		}
	}
	if kind == store.HistoryKindPollVote && len(p.SelectedOptions) > 0 {
		selected, err := json.Marshal(p.SelectedOptions)
		if err != nil {
			return store.HistoryMessage{}, err
		}
		out.SelectedOptions = selected
	}
	return out, nil
}

// historyMediaMeta keeps media metadata only. History media is never captured
// (WhatsApp media URLs in old messages are usually expired), so any storage or
// URL fields a sender put in the payload are dropped.
func historyMediaMeta(m *domain.MediaMeta) *domain.MediaMeta {
	if m == nil {
		return nil
	}
	out := &domain.MediaMeta{Mimetype: m.Mimetype, Size: m.Size, Filename: m.Filename}
	for i := range m.Items {
		out.Items = append(out.Items, *historyMediaMeta(&m.Items[i]))
	}
	return out
}

func validMessageStatus(s domain.MessageStatus) bool {
	switch s {
	case domain.MessagePending, domain.MessageSent, domain.MessageDelivered, domain.MessageRead,
		domain.MessagePlayed, domain.MessageFailed:
		return true
	}
	return false
}
