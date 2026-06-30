-- name: UpsertGroup :exec
INSERT INTO whatsapp_groups
(group_jid, subject, description, owner_jid, participant_count, is_announce,
 is_locked, created_at_wa, first_seen_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	subject           = COALESCE(VALUES(subject), subject),
	description       = COALESCE(VALUES(description), description),
	owner_jid         = COALESCE(VALUES(owner_jid), owner_jid),
	participant_count = COALESCE(VALUES(participant_count), participant_count),
	is_announce       = COALESCE(VALUES(is_announce), is_announce),
	is_locked         = COALESCE(VALUES(is_locked), is_locked),
	created_at_wa     = COALESCE(VALUES(created_at_wa), created_at_wa),
	updated_at        = VALUES(updated_at);

-- name: GetGroupByJID :one
SELECT id, group_jid, subject, description, owner_jid,
	participant_count, is_announce, is_locked, created_at_wa, first_seen_at, updated_at
FROM whatsapp_groups
WHERE group_jid = ?;

-- name: ListGroupsBySession :many
SELECT g.id, g.group_jid, g.subject, g.description, g.owner_jid,
	g.participant_count, g.is_announce, g.is_locked, g.created_at_wa, g.first_seen_at, g.updated_at
FROM whatsapp_groups g
WHERE g.group_jid IN (
	SELECT DISTINCT group_jid FROM whatsapp_group_members WHERE session_id = ?
)
ORDER BY g.id ASC;

-- name: UpsertGroupMember :exec
INSERT INTO whatsapp_group_members
(session_id, group_jid, lid, tag, role, first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	tag          = COALESCE(VALUES(tag), tag),
	role         = VALUES(role),
	last_seen_at = VALUES(last_seen_at);

-- name: ListGroupMembersByGroup :many
SELECT id, session_id, group_jid, lid, tag, role, first_seen_at, last_seen_at
FROM whatsapp_group_members
WHERE session_id = ? AND group_jid = ?
ORDER BY id ASC;

-- name: ListGroupMembersByContact :many
SELECT id, session_id, group_jid, lid, tag, role, first_seen_at, last_seen_at
FROM whatsapp_group_members
WHERE session_id = ? AND lid = ?
ORDER BY id ASC;

-- name: RemoveGroupMember :execrows
DELETE FROM whatsapp_group_members
WHERE session_id = ? AND group_jid = ? AND lid = ?;
