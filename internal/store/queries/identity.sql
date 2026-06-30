-- name: UpsertIdentity :exec
INSERT INTO whatsapp_identities
(lid, phone_number, phone_jid, name, business_name, first_seen_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	phone_number  = COALESCE(VALUES(phone_number), phone_number),
	phone_jid     = COALESCE(VALUES(phone_jid), phone_jid),
	name          = COALESCE(VALUES(name), name),
	business_name = COALESCE(VALUES(business_name), business_name),
	updated_at    = VALUES(updated_at);

-- name: FillIdentityNameByJID :exec
UPDATE whatsapp_identities
SET name = ?, updated_at = ?
WHERE (lid = ? OR phone_jid = ?) AND (name IS NULL OR name = '');

-- name: NamesForMentionJIDs :many
SELECT lid, phone_jid, name
FROM whatsapp_identities
WHERE name IS NOT NULL
  AND name <> ''
  AND (FIND_IN_SET(lid, ?) > 0 OR FIND_IN_SET(phone_jid, ?) > 0);

-- name: GetIdentityByLID :one
SELECT id, lid, phone_number, phone_jid, name, business_name, first_seen_at, updated_at
FROM whatsapp_identities
WHERE lid = ?;
