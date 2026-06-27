package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// MessageRepo is the repository for messages (§5). Rows are keyed for upsert by
// the unique (session_id, wa_message_id); the cursor for ListByChat is the
// sortable msg_<ULID> primary id.
type MessageRepo struct {
	db dbExecQuerier
}

// NewMessageRepo constructs a MessageRepo.
func NewMessageRepo(db dbExecQuerier) *MessageRepo { return &MessageRepo{db: db} }

// messageCols selects from the messages table aliased `m`, resolving the
// sender's display name from whatsapp_identities (joined as `i`, keyed by the
// sender LID). sender_name is read-only and trails the stored columns; it is
// NULL when the sender is unknown (e.g. own messages, or an unseen LID).
const messageCols = `m.id, m.session_id, m.wa_message_id, m.chat_jid, m.sender_lid,
	m.sender_jid, m.from_me, m.direction, m.type, m.body, m.quoted_message_id, m.mentions,
	m.has_media, m.media_meta, m.status, m.ack_level, m.error, m.edited, m.deleted, m.timestamp,
	m.raw_json, m.created_at, i.name AS sender_name`

// messageFrom is the FROM/JOIN clause paired with messageCols.
const messageFrom = ` FROM messages m
	LEFT JOIN whatsapp_identities i ON i.lid = m.sender_lid `

func scanMessage(s rowScanner) (domain.Message, error) {
	var (
		m         domain.Message
		mentions  []byte
		mediaMeta []byte
		rawJSON   []byte
	)
	err := s.Scan(
		&m.ID, &m.SessionID, &m.WAMessageID, &m.ChatJID, &m.SenderLID,
		&m.SenderJID, &m.FromMe, &m.Direction, &m.Type, &m.Body, &m.QuotedMessageID,
		&mentions, &m.HasMedia, &mediaMeta, &m.Status, &m.AckLevel, &m.Error,
		&m.Edited, &m.Deleted, &m.Timestamp, &rawJSON, &m.CreatedAt, &m.SenderName,
	)
	if err != nil {
		return domain.Message{}, err
	}
	if len(mentions) > 0 {
		m.Mentions = append([]byte(nil), mentions...)
	}
	if len(rawJSON) > 0 {
		m.RawJSON = append([]byte(nil), rawJSON...)
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

// Upsert inserts a message or, on the unique (session_id, wa_message_id),
// updates the content-bearing fields. This is the §7 idempotent capture: the
// same wa_message_id (e.g. redelivered event) reconciles rather than duplicates.
// Status/ack/edit/delete are owned by their dedicated methods and NOT touched on
// conflict, so a late content upsert can't regress a delivery receipt.
func (r *MessageRepo) Upsert(ctx context.Context, m domain.Message) error {
	const q = `INSERT INTO messages
(id, session_id, wa_message_id, chat_jid, sender_lid, sender_jid, from_me, direction,
 type, body, quoted_message_id, mentions, has_media, media_meta, status,
 ack_level, error, edited, deleted, timestamp, raw_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	chat_jid          = VALUES(chat_jid),
	sender_lid        = COALESCE(VALUES(sender_lid), sender_lid),
	sender_jid        = COALESCE(VALUES(sender_jid), sender_jid),
	body              = COALESCE(VALUES(body), body),
	quoted_message_id = COALESCE(VALUES(quoted_message_id), quoted_message_id),
	mentions          = COALESCE(VALUES(mentions), mentions),
	has_media         = VALUES(has_media),
	media_meta        = COALESCE(VALUES(media_meta), media_meta),
	raw_json          = COALESCE(VALUES(raw_json), raw_json)`

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

	if _, err := r.db.ExecContext(ctx, q,
		m.ID, m.SessionID, m.WAMessageID, m.ChatJID, m.SenderLID, m.SenderJID, m.FromMe,
		m.Direction, m.Type, m.Body, m.QuotedMessageID, nullableJSON(m.Mentions),
		m.HasMedia, nullableJSON(mediaMeta), m.Status, m.AckLevel, m.Error,
		m.Edited, m.Deleted, m.Timestamp, nullableJSON(m.RawJSON), m.CreatedAt,
	); err != nil {
		return fmt.Errorf("store: upsert message: %w", err)
	}
	return nil
}

// GetByWAID fetches a message by (session_id, wa_message_id). Maps no-rows to
// not_found.
func (r *MessageRepo) GetByWAID(ctx context.Context, sessionID, waMessageID string) (domain.Message, error) {
	q := "SELECT " + messageCols + messageFrom + "WHERE m.session_id = ? AND m.wa_message_id = ?"
	m, err := scanMessage(r.db.QueryRowContext(ctx, q, sessionID, waMessageID))
	if err != nil {
		return domain.Message{}, notFound(err, "message")
	}
	return m, nil
}

// UpdateStatus updates the delivery status + ack level of a message (driven by
// whatsmeow receipts, §7). ackLevel/errMsg may be nil.
func (r *MessageRepo) UpdateStatus(ctx context.Context, sessionID, waMessageID string, status domain.MessageStatus, ackLevel *int, errMsg *string) error {
	const q = `UPDATE messages SET status=?, ack_level=?, error=?
		WHERE session_id=? AND wa_message_id=?`
	res, err := r.db.ExecContext(ctx, q, status, ackLevel, errMsg, sessionID, waMessageID)
	if err != nil {
		return fmt.Errorf("store: update message status: %w", err)
	}
	return affectedOrNotFound(res, "message")
}

// MarkEdited flags a message edited and replaces its body (§9 message.edited).
func (r *MessageRepo) MarkEdited(ctx context.Context, sessionID, waMessageID, newBody string) error {
	const q = `UPDATE messages SET edited=1, body=? WHERE session_id=? AND wa_message_id=?`
	res, err := r.db.ExecContext(ctx, q, newBody, sessionID, waMessageID)
	if err != nil {
		return fmt.Errorf("store: mark message edited: %w", err)
	}
	return affectedOrNotFound(res, "message")
}

// MarkDeleted flags a message deleted/revoked (§9 message.revoked). The body is
// preserved as captured; consumers honor the deleted flag.
func (r *MessageRepo) MarkDeleted(ctx context.Context, sessionID, waMessageID string) error {
	const q = `UPDATE messages SET deleted=1 WHERE session_id=? AND wa_message_id=?`
	res, err := r.db.ExecContext(ctx, q, sessionID, waMessageID)
	if err != nil {
		return fmt.Errorf("store: mark message deleted: %w", err)
	}
	return affectedOrNotFound(res, "message")
}

// ListByChat returns a page of a chat's messages for a session (§11 GET
// /chats/{cid}/messages). Ordered by id ASC so the opaque cursor is the last
// returned id and pagination is stable under concurrent inserts.
func (r *MessageRepo) ListByChat(ctx context.Context, sessionID, chatJID, cursor string, limit int) (Page[domain.Message], error) {
	afterID, err := parseStringCursor(cursor)
	if err != nil {
		return Page[domain.Message]{}, err
	}
	limit = normLimit(limit)
	q := "SELECT " + messageCols + messageFrom + "WHERE m.session_id = ? AND m.chat_jid = ? AND m.id > ? ORDER BY m.id ASC LIMIT ?"
	rows, err := r.db.QueryContext(ctx, q, sessionID, chatJID, afterID, limit)
	if err != nil {
		return Page[domain.Message]{}, fmt.Errorf("store: list messages: %w", err)
	}
	defer rows.Close()
	var out []domain.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return Page[domain.Message]{}, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.Message]{}, err
	}
	return pageFromString(out, limit, func(m domain.Message) string { return m.ID }), nil
}
