package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// ChatRepo is the repository for chats (§5), upserted by (session_id, chat_jid).
type ChatRepo struct {
	db storedb.DBTX
	q  *storedb.Queries
}

// NewChatRepo constructs a ChatRepo.
func NewChatRepo(db storedb.DBTX) *ChatRepo { return &ChatRepo{db: db, q: storedb.New(db)} }

// Upsert inserts or updates a chat by (session_id, chat_jid). On conflict the
// name and last_message_at advance (name only when non-NULL; last_message_at
// only forward via GREATEST), unread_count is overwritten from the struct. Flags
// (archived/pinned/muted) are managed via UpdateFlags, not here, so a message-
// driven upsert doesn't clobber a user's pin/archive state.
func (r *ChatRepo) Upsert(ctx context.Context, c domain.Chat) error {
	if c.Type == domain.ChatDM {
		chatJID, err := canonicalDMChatJID(ctx, r.db, c.ChatJID)
		if err != nil {
			return err
		}
		c.ChatJID = chatJID
	}
	if err := r.q.UpsertChat(ctx, storedb.UpsertChatParams{
		SessionID:     c.SessionID,
		ChatJid:       c.ChatJID,
		Type:          storedb.ChatsType(c.Type),
		Name:          nullString(c.Name),
		LastMessageAt: nullInt64(c.LastMessageAt),
		UnreadCount:   int32(c.UnreadCount),
		Archived:      c.Archived,
		Pinned:        c.Pinned,
		MutedUntil:    nullInt64(c.MutedUntil),
	}); err != nil {
		return fmt.Errorf("store: upsert chat: %w", err)
	}
	return nil
}

// Get fetches a chat by (session_id, chat_jid). Maps no-rows to not_found.
func (r *ChatRepo) Get(ctx context.Context, sessionID, chatJID string) (domain.Chat, error) {
	row, err := r.q.GetChat(ctx, storedb.GetChatParams{
		SessionID: sessionID,
		ChatJid:   chatJID,
		Lid:       chatJID,
		PhoneJid:  sqlString(chatJID),
		ChatJid_2: chatJID,
	})
	if err != nil {
		return domain.Chat{}, notFound(err, "chat")
	}
	return chatFromParts(row.ID, row.SessionID, row.ChatJid, row.Type, row.Name, row.LastMessageAt, row.UnreadCount, row.Archived, row.Pinned, row.MutedUntil, row.Aliases)
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
	rows, err := r.q.ListChatsBySession(ctx, storedb.ListChatsBySessionParams{
		SessionID:       sessionID,
		Column2:         afterTS,
		LastMessageAt:   sqlNullInt64(afterTS),
		LastMessageAt_2: sqlNullInt64(afterTS),
		ID:              afterID,
		Limit:           int32(limit),
	})
	if err != nil {
		return Page[domain.Chat]{}, fmt.Errorf("store: list chats: %w", err)
	}
	out := make([]domain.Chat, 0, len(rows))
	for _, row := range rows {
		c, err := chatFromParts(row.ID, row.SessionID, row.ChatJid, row.Type, row.Name, row.LastMessageAt, row.UnreadCount, row.Archived, row.Pinned, row.MutedUntil, row.Aliases)
		if err != nil {
			return Page[domain.Chat]{}, err
		}
		out = append(out, c)
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
	n, err := r.q.UpdateChatFlags(ctx, storedb.UpdateChatFlagsParams{
		Archived:    archived,
		Pinned:      pinned,
		MutedUntil:  nullInt64(mutedUntil),
		UnreadCount: int32(unreadCount),
		SessionID:   sessionID,
		ChatJid:     chatJID,
	})
	if err != nil {
		return fmt.Errorf("store: update chat flags: %w", err)
	}
	return rowsAffectedOrNotFound(n, "chat")
}

// Delete removes a chat by (session_id, chat_jid) (§11 DELETE /chats/{cid}).
func (r *ChatRepo) Delete(ctx context.Context, sessionID, chatJID string) error {
	n, err := r.q.DeleteChat(ctx, storedb.DeleteChatParams{SessionID: sessionID, ChatJid: chatJID})
	if err != nil {
		return fmt.Errorf("store: delete chat: %w", err)
	}
	return rowsAffectedOrNotFound(n, "chat")
}

func chatFromParts(id uint64, sessionID, chatJID string, typ storedb.ChatsType, name sql.NullString, lastMessageAt sql.NullInt64, unreadCount int32, archived, pinned bool, mutedUntil sql.NullInt64, aliases any) (domain.Chat, error) {
	c := domain.Chat{
		ID:            id,
		SessionID:     sessionID,
		ChatJID:       chatJID,
		Type:          domain.ChatType(typ),
		Name:          stringPtrFromNull(name),
		LastMessageAt: int64PtrFromNull(lastMessageAt),
		UnreadCount:   int(unreadCount),
		Archived:      archived,
		Pinned:        pinned,
		MutedUntil:    int64PtrFromNull(mutedUntil),
	}
	rawAliases, err := bytesFromSQLValue(aliases)
	if err != nil {
		return domain.Chat{}, scanErr("chats.aliases", err)
	}
	if len(rawAliases) > 0 {
		var raw []*string
		if err := json.Unmarshal(rawAliases, &raw); err != nil {
			return domain.Chat{}, scanErr("chats.aliases", err)
		}
		for _, alias := range raw {
			if alias != nil && *alias != "" {
				c.Aliases = append(c.Aliases, *alias)
			}
		}
	}
	return c, nil
}
