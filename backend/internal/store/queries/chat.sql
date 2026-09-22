-- name: UpsertChat :exec
INSERT INTO chats (session_id, chat_jid, type, name, last_message_at, unread_count, archived, pinned, muted_until)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	name = COALESCE(VALUES(name), name),
	last_message_at = GREATEST(COALESCE(last_message_at, 0), COALESCE(VALUES(last_message_at), 0)),
	unread_count = VALUES(unread_count);

-- name: GetChat :one
SELECT c.id, c.session_id, c.chat_jid, c.type,
	COALESCE(g.subject, (SELECT COALESCE(i.name, i.business_name, i.phone_number) FROM whatsapp_identities i WHERE i.lid = c.chat_jid OR i.phone_jid = c.chat_jid ORDER BY CASE WHEN i.lid = c.chat_jid THEN 0 ELSE 1 END, i.id DESC LIMIT 1), c.name) AS name,
	c.last_message_at, c.unread_count, c.archived, c.pinned, c.muted_until,
	COALESCE((SELECT JSON_ARRAY(i.lid, i.phone_jid) FROM whatsapp_identities i WHERE c.type = 'dm' AND (i.lid = c.chat_jid OR i.phone_jid = c.chat_jid) ORDER BY CASE WHEN i.lid = c.chat_jid THEN 0 ELSE 1 END, i.id DESC LIMIT 1), '') AS aliases
FROM chats c
LEFT JOIN whatsapp_groups g ON g.group_jid = c.chat_jid
WHERE c.session_id = ? AND (c.chat_jid = ? OR EXISTS (SELECT 1 FROM whatsapp_identities i WHERE (i.lid = ? OR i.phone_jid = ?) AND (c.chat_jid = i.lid OR c.chat_jid = i.phone_jid)))
ORDER BY CASE WHEN c.chat_jid = ? THEN 0 ELSE 1 END, c.last_message_at DESC, c.id DESC
LIMIT 1;

-- name: ListChatsBySession :many
SELECT c.id, c.session_id, c.chat_jid, c.type,
	COALESCE(g.subject, (SELECT COALESCE(i.name, i.business_name, i.phone_number) FROM whatsapp_identities i WHERE i.lid = c.chat_jid OR i.phone_jid = c.chat_jid ORDER BY CASE WHEN i.lid = c.chat_jid THEN 0 ELSE 1 END, i.id DESC LIMIT 1), c.name) AS name,
	c.last_message_at, c.unread_count, c.archived, c.pinned, c.muted_until,
	COALESCE((SELECT JSON_ARRAY(i.lid, i.phone_jid) FROM whatsapp_identities i WHERE c.type = 'dm' AND (i.lid = c.chat_jid OR i.phone_jid = c.chat_jid) ORDER BY CASE WHEN i.lid = c.chat_jid THEN 0 ELSE 1 END, i.id DESC LIMIT 1), '') AS aliases
FROM chats c
LEFT JOIN whatsapp_groups g ON g.group_jid = c.chat_jid
WHERE c.session_id = ? AND c.last_message_at IS NOT NULL
	AND (? = 0 OR c.last_message_at < ? OR (c.last_message_at = ? AND c.id < ?))
ORDER BY c.last_message_at DESC, c.id DESC
LIMIT ?;

-- name: UpdateChatFlags :execrows
UPDATE chats SET archived = ?, pinned = ?, muted_until = ?, unread_count = ?
WHERE session_id = ? AND chat_jid = ?;

-- name: DeleteChat :execrows
DELETE FROM chats WHERE session_id = ? AND chat_jid = ?;
