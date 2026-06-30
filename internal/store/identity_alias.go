package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func canonicalDMChatJID(ctx context.Context, db dbExecQuerier, chatJID string) (string, error) {
	if !strings.HasSuffix(chatJID, "@s.whatsapp.net") {
		return chatJID, nil
	}
	const q = `SELECT lid
FROM whatsapp_identities
WHERE phone_jid = ? AND lid <> phone_jid AND lid LIKE '%@lid'
ORDER BY id DESC
LIMIT 2`
	rows, err := db.QueryContext(ctx, q, chatJID)
	if err != nil {
		return "", fmt.Errorf("store: resolve chat alias: %w", err)
	}
	defer rows.Close()

	var lids []string
	for rows.Next() {
		var lid string
		if err := rows.Scan(&lid); err != nil {
			return "", scanErr("identity alias", err)
		}
		lids = append(lids, lid)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("store: resolve chat alias: %w", err)
	}
	if len(lids) != 1 {
		return chatJID, nil
	}
	return lids[0], nil
}

func mergeDMChatAlias(ctx context.Context, db dbExecQuerier, sessionID, lid, phoneJID string) error {
	if lid == "" || phoneJID == "" || lid == phoneJID {
		return nil
	}
	if !strings.HasSuffix(lid, "@lid") || !strings.HasSuffix(phoneJID, "@s.whatsapp.net") {
		return nil
	}
	if sessionID == "" {
		const mergeExisting = `UPDATE chats canonical
JOIN chats alias ON alias.session_id = canonical.session_id AND alias.chat_jid = ?
SET canonical.name = COALESCE(canonical.name, alias.name),
    canonical.last_message_at = GREATEST(COALESCE(canonical.last_message_at, 0), COALESCE(alias.last_message_at, 0)),
    canonical.unread_count = GREATEST(canonical.unread_count, alias.unread_count),
    canonical.archived = canonical.archived OR alias.archived,
    canonical.pinned = canonical.pinned OR alias.pinned,
    canonical.muted_until = GREATEST(COALESCE(canonical.muted_until, 0), COALESCE(alias.muted_until, 0))
WHERE canonical.chat_jid = ?`
		if _, err := db.ExecContext(ctx, mergeExisting, phoneJID, lid); err != nil {
			return fmt.Errorf("store: merge chat aliases: %w", err)
		}
		const deleteMergedAliases = `DELETE alias FROM chats alias
JOIN chats canonical ON canonical.session_id = alias.session_id AND canonical.chat_jid = ?
WHERE alias.chat_jid = ?`
		if _, err := db.ExecContext(ctx, deleteMergedAliases, lid, phoneJID); err != nil {
			return fmt.Errorf("store: delete merged chat aliases: %w", err)
		}
		const renameAliases = `UPDATE chats alias
LEFT JOIN chats canonical ON canonical.session_id = alias.session_id AND canonical.chat_jid = ?
SET alias.chat_jid = ?
WHERE alias.chat_jid = ? AND canonical.id IS NULL`
		if _, err := db.ExecContext(ctx, renameAliases, lid, lid, phoneJID); err != nil {
			return fmt.Errorf("store: rename chat aliases: %w", err)
		}
		const updateMessages = `UPDATE messages SET chat_jid = ? WHERE chat_jid = ?`
		if _, err := db.ExecContext(ctx, updateMessages, lid, phoneJID); err != nil {
			return fmt.Errorf("store: update message chat aliases: %w", err)
		}
		const updatePolls = `UPDATE polls SET chat_jid = ? WHERE chat_jid = ?`
		if _, err := db.ExecContext(ctx, updatePolls, lid, phoneJID); err != nil {
			return fmt.Errorf("store: update poll chat aliases: %w", err)
		}
		return nil
	}

	const hasCanonical = `SELECT id FROM chats WHERE session_id = ? AND chat_jid = ? LIMIT 1`
	var id uint64
	err := db.QueryRowContext(ctx, hasCanonical, sessionID, lid).Scan(&id)
	switch {
	case err == nil:
		const mergeChat = `UPDATE chats canonical
JOIN chats alias ON alias.session_id = canonical.session_id AND alias.chat_jid = ?
SET canonical.name = COALESCE(canonical.name, alias.name),
    canonical.last_message_at = GREATEST(COALESCE(canonical.last_message_at, 0), COALESCE(alias.last_message_at, 0)),
    canonical.unread_count = GREATEST(canonical.unread_count, alias.unread_count),
    canonical.archived = canonical.archived OR alias.archived,
    canonical.pinned = canonical.pinned OR alias.pinned,
    canonical.muted_until = GREATEST(COALESCE(canonical.muted_until, 0), COALESCE(alias.muted_until, 0))
WHERE canonical.session_id = ? AND canonical.chat_jid = ?`
		if _, err := db.ExecContext(ctx, mergeChat, phoneJID, sessionID, lid); err != nil {
			return fmt.Errorf("store: merge chat alias: %w", err)
		}
		const deleteAlias = `DELETE FROM chats WHERE session_id = ? AND chat_jid = ?`
		if _, err := db.ExecContext(ctx, deleteAlias, sessionID, phoneJID); err != nil {
			return fmt.Errorf("store: delete chat alias: %w", err)
		}
	case err == sql.ErrNoRows:
		const renameChat = `UPDATE chats SET chat_jid = ? WHERE session_id = ? AND chat_jid = ?`
		if _, err := db.ExecContext(ctx, renameChat, lid, sessionID, phoneJID); err != nil {
			return fmt.Errorf("store: rename chat alias: %w", err)
		}
	default:
		return fmt.Errorf("store: check chat alias: %w", err)
	}

	const updateMessages = `UPDATE messages SET chat_jid = ? WHERE session_id = ? AND chat_jid = ?`
	if _, err := db.ExecContext(ctx, updateMessages, lid, sessionID, phoneJID); err != nil {
		return fmt.Errorf("store: update message chat alias: %w", err)
	}
	const updatePolls = `UPDATE polls SET chat_jid = ? WHERE session_id = ? AND chat_jid = ?`
	if _, err := db.ExecContext(ctx, updatePolls, lid, sessionID, phoneJID); err != nil {
		return fmt.Errorf("store: update poll chat alias: %w", err)
	}
	return nil
}
