-- name: UpsertMessage :exec
INSERT INTO messages
(id, session_id, wa_message_id, chat_jid, sender_lid, sender_jid, from_me, direction,
 type, body, quoted_message_id, mentions, has_media, media_meta, status,
 ack_level, error, edited, deleted, timestamp, raw_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(sqlc.arg(mentions), ''), ?, NULLIF(sqlc.arg(media_meta), ''), ?,
 ?, ?, ?, ?, ?, NULLIF(sqlc.arg(raw_json), ''), ?)
ON DUPLICATE KEY UPDATE
	chat_jid          = VALUES(chat_jid),
	sender_lid        = COALESCE(VALUES(sender_lid), sender_lid),
	sender_jid        = COALESCE(VALUES(sender_jid), sender_jid),
	body              = COALESCE(VALUES(body), body),
	quoted_message_id = COALESCE(VALUES(quoted_message_id), quoted_message_id),
	mentions          = COALESCE(VALUES(mentions), mentions),
	has_media         = VALUES(has_media),
	media_meta        = COALESCE(VALUES(media_meta), media_meta),
	raw_json          = COALESCE(VALUES(raw_json), raw_json);

-- name: GetMessageByWAID :one
SELECT m.id, m.session_id, m.wa_message_id, m.chat_jid, m.sender_lid,
	m.sender_jid, m.from_me, m.direction, m.type, m.body, m.quoted_message_id,
	COALESCE(m.mentions, '') AS mentions, m.has_media, COALESCE(m.media_meta, '') AS media_meta,
	m.status, m.ack_level, m.error, m.edited, m.deleted, m.timestamp,
	COALESCE(m.raw_json, '') AS raw_json, m.created_at, i.name AS sender_name
FROM messages m
LEFT JOIN whatsapp_identities i ON i.lid = m.sender_lid
WHERE m.session_id = ? AND m.wa_message_id = ?;

-- name: AdvanceReceiptStatus :exec
UPDATE messages
SET
	status = CASE
		-- A failed send is terminal. Receipt traffic for an unrelated/reused WA id
		-- must not rewrite the gateway's recorded send outcome.
		WHEN status = 'failed' THEN status
		-- Receipts may be duplicated or arrive out of order. Keep the highest
		-- lifecycle state observed instead of regressing read/played to delivered.
		WHEN FIELD(COALESCE(status, 'pending'), 'pending', 'sent', 'delivered', 'read', 'played')
			< CAST(sqlc.arg(next_status_rank) AS SIGNED)
			THEN sqlc.arg(next_status)
		ELSE status
	END,
	ack_level = CASE
		WHEN sqlc.narg(next_ack_level) IS NULL THEN ack_level
		WHEN ack_level IS NULL OR sqlc.narg(next_ack_level) > ack_level
			THEN sqlc.narg(next_ack_level)
		ELSE ack_level
	END
WHERE session_id = sqlc.arg(session_id) AND wa_message_id = sqlc.arg(wa_message_id);

-- name: MarkMessageEdited :execrows
UPDATE messages SET edited = 1, body = ?
WHERE session_id = ? AND wa_message_id = ?;

-- name: MarkMessageDeleted :execrows
UPDATE messages SET deleted = 1
WHERE session_id = ? AND wa_message_id = ?;

-- name: ListMessagesByChat :many
SELECT m.id, m.session_id, m.wa_message_id, m.chat_jid, m.sender_lid,
	m.sender_jid, m.from_me, m.direction, m.type, m.body, m.quoted_message_id,
	COALESCE(m.mentions, '') AS mentions, m.has_media, COALESCE(m.media_meta, '') AS media_meta,
	m.status, m.ack_level, m.error, m.edited, m.deleted, m.timestamp,
	COALESCE(m.raw_json, '') AS raw_json, m.created_at, i.name AS sender_name
FROM messages m
LEFT JOIN whatsapp_identities i ON i.lid = m.sender_lid
WHERE m.session_id = ? AND (sqlc.arg(message_cursor) = '' OR m.id < sqlc.arg(message_cursor)) AND (
	m.chat_jid = ? OR EXISTS (
		SELECT 1 FROM whatsapp_identities i2
		WHERE (i2.lid = ? OR i2.phone_jid = ?)
		  AND (m.chat_jid = i2.lid OR m.chat_jid = i2.phone_jid)
	)
)
ORDER BY m.id DESC
LIMIT ?;
