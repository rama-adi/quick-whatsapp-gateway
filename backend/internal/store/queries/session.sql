-- Session reads join the observed runtime and the liveness of the session's
-- gateway: the effective status a client sees is resolved from these at read
-- time (store.sessionFromRow), never copied onto wa_sessions.

-- name: CreateSession :exec
INSERT INTO wa_sessions
(id, organization_id, created_by_user_id, gateway_id, label, desired_state, start_generation,
 wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing, rate_per_min,
 rate_per_hour, last_connected_at, created_at, updated_at, history_sync_days)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: CreateSessionRuntime :exec
INSERT INTO wa_session_runtime (session_id, status, failure_reason, status_seq, status_changed_at)
VALUES (?, 'stopped', NULL, 0, ?);

-- name: GetSession :one
SELECT s.id, s.organization_id, s.created_by_user_id, s.gateway_id, s.label, s.desired_state,
	s.start_generation, s.wa_jid, s.wa_lid, s.phone_number, s.is_admin_session, s.auto_read,
	s.presence_typing, s.rate_per_min, s.rate_per_hour, s.last_connected_at, s.created_at,
	s.updated_at, s.history_sync_days, s.data_account_jid, s.account_changed_at,
	r.status AS runtime_status, r.failure_reason AS runtime_failure_reason,
	r.status_changed_at AS runtime_status_changed_at,
	(g.connected_at IS NOT NULL) AS gateway_connected
FROM wa_sessions AS s
JOIN wa_session_runtime AS r ON r.session_id = s.id
LEFT JOIN gateways AS g ON g.id = s.gateway_id AND g.deleted_at IS NULL
WHERE s.id = ?;

-- name: GetSessionByJID :one
SELECT s.id, s.organization_id, s.created_by_user_id, s.gateway_id, s.label, s.desired_state,
	s.start_generation, s.wa_jid, s.wa_lid, s.phone_number, s.is_admin_session, s.auto_read,
	s.presence_typing, s.rate_per_min, s.rate_per_hour, s.last_connected_at, s.created_at,
	s.updated_at, s.history_sync_days, s.data_account_jid, s.account_changed_at,
	r.status AS runtime_status, r.failure_reason AS runtime_failure_reason,
	r.status_changed_at AS runtime_status_changed_at,
	(g.connected_at IS NOT NULL) AS gateway_connected
FROM wa_sessions AS s
JOIN wa_session_runtime AS r ON r.session_id = s.id
LEFT JOIN gateways AS g ON g.id = s.gateway_id AND g.deleted_at IS NULL
WHERE s.wa_jid = ?;

-- name: ListSessionsByOrg :many
SELECT s.id, s.organization_id, s.created_by_user_id, s.gateway_id, s.label, s.desired_state,
	s.start_generation, s.wa_jid, s.wa_lid, s.phone_number, s.is_admin_session, s.auto_read,
	s.presence_typing, s.rate_per_min, s.rate_per_hour, s.last_connected_at, s.created_at,
	s.updated_at, s.history_sync_days, s.data_account_jid, s.account_changed_at,
	r.status AS runtime_status, r.failure_reason AS runtime_failure_reason,
	r.status_changed_at AS runtime_status_changed_at,
	(g.connected_at IS NOT NULL) AS gateway_connected
FROM wa_sessions AS s
JOIN wa_session_runtime AS r ON r.session_id = s.id
LEFT JOIN gateways AS g ON g.id = s.gateway_id AND g.deleted_at IS NULL
WHERE s.organization_id = ?
ORDER BY s.created_at DESC;

-- name: ListAllSessions :many
SELECT s.id, s.organization_id, s.created_by_user_id, s.gateway_id, s.label, s.desired_state,
	s.start_generation, s.wa_jid, s.wa_lid, s.phone_number, s.is_admin_session, s.auto_read,
	s.presence_typing, s.rate_per_min, s.rate_per_hour, s.last_connected_at, s.created_at,
	s.updated_at, s.history_sync_days, s.data_account_jid, s.account_changed_at,
	r.status AS runtime_status, r.failure_reason AS runtime_failure_reason,
	r.status_changed_at AS runtime_status_changed_at,
	(g.connected_at IS NOT NULL) AS gateway_connected
FROM wa_sessions AS s
JOIN wa_session_runtime AS r ON r.session_id = s.id
LEFT JOIN gateways AS g ON g.id = s.gateway_id AND g.deleted_at IS NULL
ORDER BY s.created_at DESC;

-- name: ListSessionsByGateway :many
SELECT s.id, s.organization_id, s.created_by_user_id, s.gateway_id, s.label, s.desired_state,
	s.start_generation, s.wa_jid, s.wa_lid, s.phone_number, s.is_admin_session, s.auto_read,
	s.presence_typing, s.rate_per_min, s.rate_per_hour, s.last_connected_at, s.created_at,
	s.updated_at, s.history_sync_days, s.data_account_jid, s.account_changed_at,
	r.status AS runtime_status, r.failure_reason AS runtime_failure_reason,
	r.status_changed_at AS runtime_status_changed_at,
	(g.connected_at IS NOT NULL) AS gateway_connected
FROM wa_sessions AS s
JOIN wa_session_runtime AS r ON r.session_id = s.id
LEFT JOIN gateways AS g ON g.id = s.gateway_id AND g.deleted_at IS NULL
WHERE s.gateway_id = ?
ORDER BY s.created_at ASC;

-- name: CountSessionsByGateway :one
SELECT COUNT(*) FROM wa_sessions WHERE gateway_id = ?;

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
