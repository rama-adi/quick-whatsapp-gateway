-- name: ListContactsAnywhere :many
SELECT i.id, i.lid, i.phone_number, i.name, i.business_name,
	EXISTS (SELECT 1 FROM chats ch WHERE ch.session_id = sqlc.arg(session_id) AND ch.type = 'dm' AND (ch.chat_jid = i.lid OR (i.phone_jid IS NOT NULL AND ch.chat_jid = i.phone_jid))) AS in_dm,
	EXISTS (SELECT 1 FROM whatsapp_group_members gm WHERE gm.session_id = sqlc.arg(session_id) AND gm.lid = i.lid) AS in_group
FROM whatsapp_identities i
WHERE i.id > sqlc.arg(after_id)
	AND (EXISTS (SELECT 1 FROM chats ch WHERE ch.session_id = sqlc.arg(session_id) AND ch.type = 'dm' AND (ch.chat_jid = i.lid OR (i.phone_jid IS NOT NULL AND ch.chat_jid = i.phone_jid)))
		OR EXISTS (SELECT 1 FROM whatsapp_group_members gm WHERE gm.session_id = sqlc.arg(session_id) AND gm.lid = i.lid))
ORDER BY i.id ASC
LIMIT ?;

-- name: ListContactsAnywhereSearch :many
SELECT i.id, i.lid, i.phone_number, i.name, i.business_name,
	EXISTS (SELECT 1 FROM chats ch WHERE ch.session_id = sqlc.arg(session_id) AND ch.type = 'dm' AND (ch.chat_jid = i.lid OR (i.phone_jid IS NOT NULL AND ch.chat_jid = i.phone_jid))) AS in_dm,
	EXISTS (SELECT 1 FROM whatsapp_group_members gm WHERE gm.session_id = sqlc.arg(session_id) AND gm.lid = i.lid) AS in_group
FROM whatsapp_identities i
WHERE i.id > sqlc.arg(after_id)
	AND (EXISTS (SELECT 1 FROM chats ch WHERE ch.session_id = sqlc.arg(session_id) AND ch.type = 'dm' AND (ch.chat_jid = i.lid OR (i.phone_jid IS NOT NULL AND ch.chat_jid = i.phone_jid)))
		OR EXISTS (SELECT 1 FROM whatsapp_group_members gm WHERE gm.session_id = sqlc.arg(session_id) AND gm.lid = i.lid))
	AND i.name LIKE sqlc.arg(name_pattern)
ORDER BY i.id ASC
LIMIT ?;

-- name: ListContactsDM :many
SELECT i.id, i.lid, i.phone_number, i.name, i.business_name, TRUE AS in_dm,
	EXISTS (SELECT 1 FROM whatsapp_group_members gm WHERE gm.session_id = sqlc.arg(session_id) AND gm.lid = i.lid) AS in_group
FROM whatsapp_identities i
WHERE i.id > sqlc.arg(after_id)
	AND EXISTS (SELECT 1 FROM chats ch WHERE ch.session_id = sqlc.arg(session_id) AND ch.type = 'dm' AND (ch.chat_jid = i.lid OR (i.phone_jid IS NOT NULL AND ch.chat_jid = i.phone_jid)))
ORDER BY i.id ASC
LIMIT ?;

-- name: ListContactsDMSearch :many
SELECT i.id, i.lid, i.phone_number, i.name, i.business_name, TRUE AS in_dm,
	EXISTS (SELECT 1 FROM whatsapp_group_members gm WHERE gm.session_id = sqlc.arg(session_id) AND gm.lid = i.lid) AS in_group
FROM whatsapp_identities i
WHERE i.id > sqlc.arg(after_id)
	AND EXISTS (SELECT 1 FROM chats ch WHERE ch.session_id = sqlc.arg(session_id) AND ch.type = 'dm' AND (ch.chat_jid = i.lid OR (i.phone_jid IS NOT NULL AND ch.chat_jid = i.phone_jid)))
	AND i.name LIKE sqlc.arg(name_pattern)
ORDER BY i.id ASC
LIMIT ?;

-- name: ListContactsGroup :many
SELECT i.id, i.lid, i.phone_number, i.name, i.business_name,
	EXISTS (SELECT 1 FROM chats ch WHERE ch.session_id = sqlc.arg(session_id) AND ch.type = 'dm' AND (ch.chat_jid = i.lid OR (i.phone_jid IS NOT NULL AND ch.chat_jid = i.phone_jid))) AS in_dm,
	TRUE AS in_group
FROM whatsapp_identities i
WHERE i.id > sqlc.arg(after_id)
	AND EXISTS (SELECT 1 FROM whatsapp_group_members gm WHERE gm.session_id = sqlc.arg(session_id) AND gm.lid = i.lid)
ORDER BY i.id ASC
LIMIT ?;

-- name: ListContactsGroupSearch :many
SELECT i.id, i.lid, i.phone_number, i.name, i.business_name,
	EXISTS (SELECT 1 FROM chats ch WHERE ch.session_id = sqlc.arg(session_id) AND ch.type = 'dm' AND (ch.chat_jid = i.lid OR (i.phone_jid IS NOT NULL AND ch.chat_jid = i.phone_jid))) AS in_dm,
	TRUE AS in_group
FROM whatsapp_identities i
WHERE i.id > sqlc.arg(after_id)
	AND EXISTS (SELECT 1 FROM whatsapp_group_members gm WHERE gm.session_id = sqlc.arg(session_id) AND gm.lid = i.lid)
	AND i.name LIKE sqlc.arg(name_pattern)
ORDER BY i.id ASC
LIMIT ?;

-- name: ListContactsByGroup :many
SELECT i.id, i.lid, i.phone_number, i.name, i.business_name,
	EXISTS (SELECT 1 FROM chats ch WHERE ch.session_id = sqlc.arg(session_id) AND ch.type = 'dm' AND (ch.chat_jid = i.lid OR (i.phone_jid IS NOT NULL AND ch.chat_jid = i.phone_jid))) AS in_dm,
	TRUE AS in_group
FROM whatsapp_identities i
WHERE i.id > sqlc.arg(after_id)
	AND EXISTS (SELECT 1 FROM whatsapp_group_members gm WHERE gm.session_id = sqlc.arg(session_id) AND gm.lid = i.lid AND gm.group_jid = sqlc.arg(group_jid))
ORDER BY i.id ASC
LIMIT ?;

-- name: ListContactsByGroupSearch :many
SELECT i.id, i.lid, i.phone_number, i.name, i.business_name,
	EXISTS (SELECT 1 FROM chats ch WHERE ch.session_id = sqlc.arg(session_id) AND ch.type = 'dm' AND (ch.chat_jid = i.lid OR (i.phone_jid IS NOT NULL AND ch.chat_jid = i.phone_jid))) AS in_dm,
	TRUE AS in_group
FROM whatsapp_identities i
WHERE i.id > sqlc.arg(after_id)
	AND EXISTS (SELECT 1 FROM whatsapp_group_members gm WHERE gm.session_id = sqlc.arg(session_id) AND gm.lid = i.lid AND gm.group_jid = sqlc.arg(group_jid))
	AND i.name LIKE sqlc.arg(name_pattern)
ORDER BY i.id ASC
LIMIT ?;

-- name: ContactSeenInDM :one
SELECT EXISTS (
	SELECT 1 FROM chats
	WHERE session_id = sqlc.arg(session_id) AND type = 'dm'
		AND (chat_jid = sqlc.arg(lid) OR (sqlc.arg(phone_jid) <> '' AND chat_jid = sqlc.arg(phone_jid)))
);
