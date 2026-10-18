-- name: CreateSession :exec
INSERT INTO wa_sessions
(id, organization_id, created_by_user_id, gateway_id, label, status, wa_jid, wa_lid,
 phone_number, is_admin_session, auto_read, presence_typing, rate_per_min, rate_per_hour,
 last_connected_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSession :one
SELECT id, organization_id, created_by_user_id, gateway_id, label, status,
	wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing,
	rate_per_min, rate_per_hour, last_connected_at, created_at, updated_at,
	history_sync_days, data_account_jid, account_changed_at
FROM wa_sessions
WHERE id = ?;

-- name: GetSessionByJID :one
SELECT id, organization_id, created_by_user_id, gateway_id, label, status,
	wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing,
	rate_per_min, rate_per_hour, last_connected_at, created_at, updated_at,
	history_sync_days, data_account_jid, account_changed_at
FROM wa_sessions
WHERE wa_jid = ?;

-- name: ListSessionsByOrg :many
SELECT id, organization_id, created_by_user_id, gateway_id, label, status,
	wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing,
	rate_per_min, rate_per_hour, last_connected_at, created_at, updated_at,
	history_sync_days, data_account_jid, account_changed_at
FROM wa_sessions
WHERE organization_id = ?
ORDER BY created_at DESC;

-- name: ListAllSessions :many
SELECT id, organization_id, created_by_user_id, gateway_id, label, status,
	wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing,
	rate_per_min, rate_per_hour, last_connected_at, created_at, updated_at,
	history_sync_days, data_account_jid, account_changed_at
FROM wa_sessions
ORDER BY created_at DESC;

-- name: ListSessionsByGateway :many
SELECT id, organization_id, created_by_user_id, gateway_id, label, status,
	wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing,
	rate_per_min, rate_per_hour, last_connected_at, created_at, updated_at,
	history_sync_days, data_account_jid, account_changed_at
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

-- name: ClearSessionPairing :execrows
UPDATE wa_sessions
SET status = 'logged_out', wa_jid = NULL, wa_lid = NULL, phone_number = NULL, updated_at = ?
WHERE id = ?;

-- name: AttachSessionPairing :execrows
-- account_changed_at is assigned before data_account_jid (MySQL applies SET
-- left to right), so it compares against the previously paired account.
UPDATE wa_sessions
SET account_changed_at = CASE
		-- NULL on either side compares as NULL, which keeps the stored value.
		WHEN data_account_jid <> sqlc.narg(account_jid) THEN sqlc.narg(changed_at)
		ELSE account_changed_at
	END,
	data_account_jid = COALESCE(sqlc.narg(account_jid), data_account_jid),
	wa_jid = sqlc.narg(wa_jid), wa_lid = sqlc.narg(wa_lid), phone_number = sqlc.narg(phone_number),
	updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: DeleteSession :execrows
DELETE FROM wa_sessions WHERE id = ?;
