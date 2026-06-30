-- name: ResolveCanonicalDMChatJID :many
SELECT lid
FROM whatsapp_identities
WHERE phone_jid = ? AND lid <> phone_jid AND lid LIKE '%@lid'
ORDER BY id DESC
LIMIT 2;

-- name: MergeExistingDMChatAliases :exec
UPDATE chats canonical
JOIN chats alias ON alias.session_id = canonical.session_id
SET canonical.name = COALESCE(canonical.name, alias.name),
    canonical.last_message_at = GREATEST(COALESCE(canonical.last_message_at, 0), COALESCE(alias.last_message_at, 0)),
    canonical.unread_count = GREATEST(canonical.unread_count, alias.unread_count),
    canonical.archived = canonical.archived OR alias.archived,
    canonical.pinned = canonical.pinned OR alias.pinned,
    canonical.muted_until = GREATEST(COALESCE(canonical.muted_until, 0), COALESCE(alias.muted_until, 0))
WHERE alias.chat_jid = ? AND canonical.chat_jid = ?;

-- name: DeleteMergedDMChatAliases :exec
DELETE alias FROM chats alias
JOIN chats canonical ON canonical.session_id = alias.session_id
WHERE canonical.chat_jid = ? AND alias.chat_jid = ?;

-- name: RenameDMChatAliasesWithoutCanonical :exec
UPDATE chats alias
SET alias.chat_jid = ?
WHERE alias.chat_jid = ?
  AND NOT EXISTS (
    SELECT 1 FROM chats canonical
    WHERE canonical.session_id = alias.session_id AND canonical.chat_jid = ?
  );

-- name: UpdateMessageDMChatAliases :exec
UPDATE messages SET chat_jid = ? WHERE chat_jid = ?;

-- name: UpdatePollDMChatAliases :exec
UPDATE polls SET chat_jid = ? WHERE chat_jid = ?;

-- name: GetCanonicalChatIDForAliasMerge :one
SELECT id FROM chats WHERE session_id = ? AND chat_jid = ? LIMIT 1;

-- name: MergeSessionDMChatAlias :exec
UPDATE chats canonical
JOIN chats alias ON alias.session_id = canonical.session_id
SET canonical.name = COALESCE(canonical.name, alias.name),
    canonical.last_message_at = GREATEST(COALESCE(canonical.last_message_at, 0), COALESCE(alias.last_message_at, 0)),
    canonical.unread_count = GREATEST(canonical.unread_count, alias.unread_count),
    canonical.archived = canonical.archived OR alias.archived,
    canonical.pinned = canonical.pinned OR alias.pinned,
    canonical.muted_until = GREATEST(COALESCE(canonical.muted_until, 0), COALESCE(alias.muted_until, 0))
WHERE alias.chat_jid = ? AND canonical.session_id = ? AND canonical.chat_jid = ?;

-- name: DeleteSessionDMChatAlias :exec
DELETE FROM chats WHERE session_id = ? AND chat_jid = ?;

-- name: RenameSessionDMChatAlias :exec
UPDATE chats SET chat_jid = ? WHERE session_id = ? AND chat_jid = ?;

-- name: UpdateSessionMessageDMChatAliases :exec
UPDATE messages SET chat_jid = ? WHERE session_id = ? AND chat_jid = ?;

-- name: UpdateSessionPollDMChatAliases :exec
UPDATE polls SET chat_jid = ? WHERE session_id = ? AND chat_jid = ?;
