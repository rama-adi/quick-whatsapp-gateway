package store

import (
	"context"
	"fmt"

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
// its real subject rather than whatever per-message name landed in chats.name.
// For non-group chats g.subject is NULL and c.name is used unchanged.
const chatCols = `c.id, c.session_id, c.chat_jid, c.type,
	COALESCE(g.subject, c.name) AS name, c.last_message_at,
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

// ListBySession returns a page of chats for a session, ordered by id ASC for a
// stable cursor. (Recency ordering for the viewer can be layered on later; the
// cursor here is the surrogate id per §11.)
func (r *ChatRepo) ListBySession(ctx context.Context, sessionID, cursor string, limit int) (Page[domain.Chat], error) {
	afterID, err := parseCursor(cursor)
	if err != nil {
		return Page[domain.Chat]{}, err
	}
	limit = normLimit(limit)
	q := "SELECT " + chatCols + chatFrom + "WHERE c.session_id = ? AND c.id > ? ORDER BY c.id ASC LIMIT ?"
	rows, err := r.db.QueryContext(ctx, q, sessionID, afterID, limit)
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
	return pageFrom(out, limit, func(c domain.Chat) uint64 { return c.ID }), nil
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
