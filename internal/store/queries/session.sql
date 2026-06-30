-- name: CreateSession :exec
INSERT INTO wa_sessions
(id, organization_id, created_by_user_id, gateway_id, label, status, wa_jid, wa_lid,
 phone_number, is_admin_session, auto_read, presence_typing, rate_per_min, rate_per_hour,
 last_connected_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSession :one
SELECT id, organization_id, created_by_user_id, gateway_id, label, status,
	wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing,
	rate_per_min, rate_per_hour, last_connected_at, created_at, updated_at
FROM wa_sessions
WHERE id = ?;

-- name: GetSessionByJID :one
SELECT id, organization_id, created_by_user_id, gateway_id, label, status,
	wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing,
	rate_per_min, rate_per_hour, last_connected_at, created_at, updated_at
FROM wa_sessions
WHERE wa_jid = ?;

-- name: ListSessionsByOrg :many
SELECT id, organization_id, created_by_user_id, gateway_id, label, status,
	wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing,
	rate_per_min, rate_per_hour, last_connected_at, created_at, updated_at
FROM wa_sessions
WHERE organization_id = ?
ORDER BY created_at DESC;

-- name: ListAllSessions :many
SELECT id, organization_id, created_by_user_id, gateway_id, label, status,
	wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing,
	rate_per_min, rate_per_hour, last_connected_at, created_at, updated_at
FROM wa_sessions
ORDER BY created_at DESC;

-- name: ListSessionsByGateway :many
SELECT id, organization_id, created_by_user_id, gateway_id, label, status,
	wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing,
	rate_per_min, rate_per_hour, last_connected_at, created_at, updated_at
FROM wa_sessions
WHERE gateway_id = ?
ORDER BY created_at ASC;

-- name: CountSessionsByGateway :one
SELECT COUNT(*) FROM wa_sessions WHERE gateway_id = ?;

-- name: UpdateSession :execrows
UPDATE wa_sessions SET
	label = ?, status = ?, wa_jid = ?, wa_lid = ?, phone_number = ?, auto_read = ?,
	presence_typing = ?, rate_per_min = ?, rate_per_hour = ?, last_connected_at = ?,
	updated_at = ?
WHERE id = ?;

-- name: UpdateSessionStatus :execrows
UPDATE wa_sessions
SET status = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteSession :execrows
DELETE FROM wa_sessions WHERE id = ?;
