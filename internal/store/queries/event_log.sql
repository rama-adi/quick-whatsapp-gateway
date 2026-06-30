-- name: AppendEventLog :execresult
INSERT INTO event_log (event_id, organization_id, session_id, type, payload, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListEventLogSinceForOrg :many
SELECT id, event_id, organization_id, session_id, type, payload, created_at
FROM event_log
WHERE organization_id = ? AND id > ?
ORDER BY id ASC
LIMIT ?;

-- name: ListEventLogSinceForSession :many
SELECT id, event_id, organization_id, session_id, type, payload, created_at
FROM event_log
WHERE organization_id = ? AND session_id = ? AND id > ?
ORDER BY id ASC
LIMIT ?;

-- name: GetEventLogByEventID :one
SELECT id, event_id, organization_id, session_id, type, payload, created_at
FROM event_log
WHERE event_id = ?;
