package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// ChatRepo is the repository for chats (§5), upserted by (session_id, chat_jid).
type ChatRepo struct {
	db dbExecQuerier
}

// NewChatRepo constructs a ChatRepo.
func NewChatRepo(db dbExecQuerier) *ChatRepo { return &ChatRepo{db: db} }

// chatCols selects from the chats table aliased `c`, resolving the display name
// for group chats from whatsapp_groups.subject (joined as `g`) so a group shows
// its real subject, and for DMs from whatsapp_identities with a scalar lookup so
// LID rows show the best known push/business/phone name without duplicating rows
// when more than one identity shares a phone_jid.
const chatCols = `c.id, c.session_id, c.chat_jid, c.type,
	COALESCE(g.subject, (
		SELECT COALESCE(i.name, i.business_name, i.phone_number)
		FROM whatsapp_identities i
		WHERE i.lid = c.chat_jid OR i.phone_jid = c.chat_jid
		ORDER BY CASE WHEN i.lid = c.chat_jid THEN 0 ELSE 1 END, i.id DESC
		LIMIT 1
	), c.name) AS name, c.last_message_at,
	c.unread_count, c.archived, c.pinned, c.muted_until`

// chatFrom is the FROM/JOIN clause paired with chatCols.
const chatFrom = ` FROM chats c
	LEFT JOIN whatsapp_groups g ON g.group_jid = c.chat_jid `

func scanChat(s rowScanner) (domain.Chat, error) {
	var c domain.Chat
	err := s.Scan(
		&c.ID, &c.SessionID, &c.ChatJID, &c.Type, &c.Name, &c.LastMessageAt,
		&c.UnreadCount, &c.Archived, &c.Pinned, &c.MutedUntil,
	)
	if err != nil {
		return domain.Chat{}, err
	}
	return c, nil
}

// Upsert inserts or updates a chat by (session_id, chat_jid). On conflict the
// name and last_message_at advance (name only when non-NULL; last_message_at
// only forward via GREATEST), unread_count is overwritten from the struct. Flags
// (archived/pinned/muted) are managed via UpdateFlags, not here, so a message-
// driven upsert doesn't clobber a user's pin/archive state.
func (r *ChatRepo) Upsert(ctx context.Context, c domain.Chat) error {
	const q = `INSERT INTO chats
(session_id, chat_jid, type, name, last_message_at, unread_count, archived, pinned, muted_until)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	name            = COALESCE(VALUES(name), name),
	last_message_at = GREATEST(COALESCE(last_message_at, 0), COALESCE(VALUES(last_message_at), 0)),
	unread_count    = VALUES(unread_count)`
	if _, err := r.db.ExecContext(ctx, q,
		c.SessionID, c.ChatJID, c.Type, c.Name, c.LastMessageAt, c.UnreadCount,
		c.Archived, c.Pinned, c.MutedUntil,
	); err != nil {
		return fmt.Errorf("store: upsert chat: %w", err)
	}
	return nil
}

// Get fetches a chat by (session_id, chat_jid). Maps no-rows to not_found.
func (r *ChatRepo) Get(ctx context.Context, sessionID, chatJID string) (domain.Chat, error) {
	q := "SELECT " + chatCols + chatFrom + "WHERE c.session_id = ? AND c.chat_jid = ?"
	c, err := scanChat(r.db.QueryRowContext(ctx, q, sessionID, chatJID))
	if err != nil {
		return domain.Chat{}, notFound(err, "chat")
	}
	return c, nil
}

// ListBySession returns a page of real conversations for a session, newest
// activity first. Rows with no message timestamp are intentionally omitted:
// found-but-never-messaged users belong in the contacts/new-chat picker, not the
// chat inbox. The cursor is opaque "lastMessageAt:id" for the descending order.
func (r *ChatRepo) ListBySession(ctx context.Context, sessionID, cursor string, limit int) (Page[domain.Chat], error) {
	afterTS, afterID, err := parseChatListCursor(cursor)
	if err != nil {
		return Page[domain.Chat]{}, err
	}
	limit = normLimit(limit)
	q := "SELECT " + chatCols + chatFrom + `WHERE c.session_id = ? AND c.last_message_at IS NOT NULL
		AND (? = 0 OR c.last_message_at < ? OR (c.last_message_at = ? AND c.id < ?))
		ORDER BY c.last_message_at DESC, c.id DESC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, sessionID, afterTS, afterTS, afterTS, afterID, limit)
	if err != nil {
		return Page[domain.Chat]{}, fmt.Errorf("store: list chats: %w", err)
	}
	defer rows.Close()
	var out []domain.Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return Page[domain.Chat]{}, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.Chat]{}, err
	}
	next := ""
	if len(out) == limit && len(out) > 0 {
		last := out[len(out)-1]
		if last.LastMessageAt != nil {
			next = encodeChatListCursor(*last.LastMessageAt, last.ID)
		}
	}
	return Page[domain.Chat]{Items: out, NextCursor: next}, nil
}

func parseChatListCursor(cursor string) (int64, uint64, error) {
	if cursor == "" {
		return 0, 0, nil
	}
	if strings.TrimSpace(cursor) != cursor {
		return 0, 0, domain.ErrValidation("invalid cursor")
	}
	parts := strings.Split(cursor, ":")
	if len(parts) != 2 {
		return 0, 0, domain.ErrValidation("invalid cursor")
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || ts < 0 {
		return 0, 0, domain.ErrValidation("invalid cursor")
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, 0, domain.ErrValidation("invalid cursor")
	}
	return ts, id, nil
}

func encodeChatListCursor(ts int64, id uint64) string {
	return strconv.FormatInt(ts, 10) + ":" + strconv.FormatUint(id, 10)
}

// UpdateFlags sets the user-managed chat flags (§11 PATCH archive/pin/mute and
// read). All four are written from the struct so the caller passes the full
// desired state.
func (r *ChatRepo) UpdateFlags(ctx context.Context, sessionID, chatJID string, archived, pinned bool, mutedUntil *int64, unreadCount int) error {
	const q = `UPDATE chats SET archived=?, pinned=?, muted_until=?, unread_count=?
		WHERE session_id=? AND chat_jid=?`
	res, err := r.db.ExecContext(ctx, q, archived, pinned, mutedUntil, unreadCount, sessionID, chatJID)
	if err != nil {
		return fmt.Errorf("store: update chat flags: %w", err)
	}
	return affectedOrNotFound(res, "chat")
}

// Delete removes a chat by (session_id, chat_jid) (§11 DELETE /chats/{cid}).
func (r *ChatRepo) Delete(ctx context.Context, sessionID, chatJID string) error {
	const q = "DELETE FROM chats WHERE session_id=? AND chat_jid=?"
	res, err := r.db.ExecContext(ctx, q, sessionID, chatJID)
	if err != nil {
		return fmt.Errorf("store: delete chat: %w", err)
	}
	return affectedOrNotFound(res, "chat")
}
