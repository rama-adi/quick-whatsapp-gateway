package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// MessageRepo is the repository for messages (§5). Rows are keyed for upsert by
// the unique (session_id, wa_message_id); the cursor for ListByChat is the
// sortable msg_<ULID> primary id.
type MessageRepo struct {
	db storedb.DBTX
	q  *storedb.Queries
}

// NewMessageRepo constructs a MessageRepo.
func NewMessageRepo(db storedb.DBTX) *MessageRepo { return &MessageRepo{db: db, q: storedb.New(db)} }

// Upsert inserts a message or, on the unique (session_id, wa_message_id),
// updates the content-bearing fields. This is the §7 idempotent capture: the
// same wa_message_id (e.g. redelivered event) reconciles rather than duplicates.
// Status/ack/edit/delete are owned by their dedicated methods and NOT touched on
// conflict, so a late content upsert can't regress a delivery receipt.
func (r *MessageRepo) Upsert(ctx context.Context, m domain.Message) error {
	chatJID, err := canonicalDMChatJID(ctx, r.db, m.ChatJID)
	if err != nil {
		return err
	}
	m.ChatJID = chatJID

	var mediaMeta []byte
	if m.MediaMeta != nil {
		b, err := json.Marshal(m.MediaMeta)
		if err != nil {
			return fmt.Errorf("store: marshal media_meta: %w", err)
		}
		mediaMeta = b
	}
	if m.ID == "" {
		m.ID = domain.NewMessageID()
	}

	if err := r.q.UpsertMessage(ctx, storedb.UpsertMessageParams{
		ID:              m.ID,
		SessionID:       m.SessionID,
		WaMessageID:     m.WAMessageID,
		ChatJid:         m.ChatJID,
		SenderLid:       nullString(m.SenderLID),
		SenderJid:       nullString(m.SenderJID),
		FromMe:          m.FromMe,
		Direction:       storedb.MessagesDirection(m.Direction),
		Type:            m.Type,
		Body:            nullString(m.Body),
		QuotedMessageID: nullString(m.QuotedMessageID),
		Mentions:        nullableJSON(m.Mentions),
		HasMedia:        m.HasMedia,
		MediaMeta:       nullableJSON(mediaMeta),
		Status:          nullMessageStatus(m.Status),
		AckLevel:        nullInt32FromPtr(m.AckLevel),
		Error:           nullString(m.Error),
		Edited:          m.Edited,
		Deleted:         m.Deleted,
		Timestamp:       m.Timestamp,
		RawJson:         nullableJSON(m.RawJSON),
		CreatedAt:       m.CreatedAt,
	}); err != nil {
		return fmt.Errorf("store: upsert message: %w", err)
	}
	return nil
}

// GetByWAID fetches a message by (session_id, wa_message_id). Maps no-rows to
// not_found.
func (r *MessageRepo) GetByWAID(ctx context.Context, sessionID, waMessageID string) (domain.Message, error) {
	row, err := r.q.GetMessageByWAID(ctx, storedb.GetMessageByWAIDParams{SessionID: sessionID, WaMessageID: waMessageID})
	if err != nil {
		return domain.Message{}, notFound(err, "message")
	}
	return messageFromParts(row.ID, row.SessionID, row.WaMessageID, row.ChatJid, row.SenderLid, row.SenderJid, row.FromMe, row.Direction, row.Type, row.Body, row.QuotedMessageID, row.Mentions, row.HasMedia, row.MediaMeta, row.Status, row.AckLevel, row.Error, row.Edited, row.Deleted, row.Timestamp, row.RawJson, row.CreatedAt, row.SenderName)
}

// UpdateStatus updates the delivery status + ack level of a message (driven by
// whatsmeow receipts, §7). ackLevel/errMsg may be nil.
func (r *MessageRepo) UpdateStatus(ctx context.Context, sessionID, waMessageID string, status domain.MessageStatus, ackLevel *int, errMsg *string) error {
	n, err := r.q.UpdateMessageStatus(ctx, storedb.UpdateMessageStatusParams{
		Status:      nullMessageStatus(&status),
		AckLevel:    nullInt32FromPtr(ackLevel),
		Error:       nullString(errMsg),
		SessionID:   sessionID,
		WaMessageID: waMessageID,
	})
	if err != nil {
		return fmt.Errorf("store: update message status: %w", err)
	}
	return rowsAffectedOrNotFound(n, "message")
}

// MarkEdited flags a message edited and replaces its body (§9 message.edited).
func (r *MessageRepo) MarkEdited(ctx context.Context, sessionID, waMessageID, newBody string) error {
	n, err := r.q.MarkMessageEdited(ctx, storedb.MarkMessageEditedParams{
		Body:        sqlString(newBody),
		SessionID:   sessionID,
		WaMessageID: waMessageID,
	})
	if err != nil {
		return fmt.Errorf("store: mark message edited: %w", err)
	}
	return rowsAffectedOrNotFound(n, "message")
}

// MarkDeleted flags a message deleted/revoked (§9 message.revoked). The body is
// preserved as captured; consumers honor the deleted flag.
func (r *MessageRepo) MarkDeleted(ctx context.Context, sessionID, waMessageID string) error {
	n, err := r.q.MarkMessageDeleted(ctx, storedb.MarkMessageDeletedParams{SessionID: sessionID, WaMessageID: waMessageID})
	if err != nil {
		return fmt.Errorf("store: mark message deleted: %w", err)
	}
	return rowsAffectedOrNotFound(n, "message")
}

// ListByChat returns a page of a chat's messages for a session (§11 GET
// /chats/{cid}/messages). Page 0 is newest-first; the opaque cursor is the last
// returned id and the next page returns older rows (`id < cursor`).
func (r *MessageRepo) ListByChat(ctx context.Context, sessionID, chatJID, cursor string, limit int) (Page[domain.Message], error) {
	afterID, err := parseStringCursor(cursor)
	if err != nil {
		return Page[domain.Message]{}, err
	}
	limit = normLimit(limit)
	rows, err := r.q.ListMessagesByChat(ctx, storedb.ListMessagesByChatParams{
		SessionID:     sessionID,
		MessageCursor: afterID,
		ChatJid:       chatJID,
		Lid:           chatJID,
		PhoneJid:      sqlString(chatJID),
		Limit:         int32(limit),
	})
	if err != nil {
		return Page[domain.Message]{}, fmt.Errorf("store: list messages: %w", err)
	}
	out := make([]domain.Message, 0, len(rows))
	for _, row := range rows {
		m, err := messageFromParts(row.ID, row.SessionID, row.WaMessageID, row.ChatJid, row.SenderLid, row.SenderJid, row.FromMe, row.Direction, row.Type, row.Body, row.QuotedMessageID, row.Mentions, row.HasMedia, row.MediaMeta, row.Status, row.AckLevel, row.Error, row.Edited, row.Deleted, row.Timestamp, row.RawJson, row.CreatedAt, row.SenderName)
		if err != nil {
			return Page[domain.Message]{}, err
		}
		out = append(out, m)
	}
	return pageFromString(out, limit, func(m domain.Message) string { return m.ID }), nil
}

func messageFromParts(id, sessionID, waMessageID, chatJID string, senderLID, senderJID sql.NullString, fromMe bool, direction storedb.MessagesDirection, typ string, body, quotedMessageID sql.NullString, mentions json.RawMessage, hasMedia bool, mediaMeta json.RawMessage, status storedb.NullMessagesStatus, ackLevel sql.NullInt32, errMsg sql.NullString, edited, deleted bool, timestamp int64, rawJSON json.RawMessage, createdAt int64, senderName sql.NullString) (domain.Message, error) {
	m := domain.Message{
		ID:              id,
		SessionID:       sessionID,
		WAMessageID:     waMessageID,
		ChatJID:         chatJID,
		SenderLID:       stringPtrFromNull(senderLID),
		SenderJID:       stringPtrFromNull(senderJID),
		SenderName:      stringPtrFromNull(senderName),
		FromMe:          fromMe,
		Direction:       domain.MessageDirection(direction),
		Type:            typ,
		Body:            stringPtrFromNull(body),
		QuotedMessageID: stringPtrFromNull(quotedMessageID),
		Mentions:        jsonOrNil(mentions),
		HasMedia:        hasMedia,
		Status:          messageStatusPtr(status),
		AckLevel:        intPtrFromNull32(ackLevel),
		Error:           stringPtrFromNull(errMsg),
		Edited:          edited,
		Deleted:         deleted,
		Timestamp:       timestamp,
		RawJSON:         jsonOrNil(rawJSON),
		CreatedAt:       createdAt,
	}
	if len(mediaMeta) > 0 {
		mm := &domain.MediaMeta{}
		if err := json.Unmarshal(mediaMeta, mm); err != nil {
			return domain.Message{}, scanErr("messages.media_meta", err)
		}
		m.MediaMeta = mm
	}
	return m, nil
}

func messageStatusPtr(s storedb.NullMessagesStatus) *domain.MessageStatus {
	if !s.Valid {
		return nil
	}
	v := domain.MessageStatus(s.MessagesStatus)
	return &v
}

func nullMessageStatus(s *domain.MessageStatus) storedb.NullMessagesStatus {
	if s == nil {
		return storedb.NullMessagesStatus{}
	}
	return storedb.NullMessagesStatus{MessagesStatus: storedb.MessagesStatus(*s), Valid: true}
}
