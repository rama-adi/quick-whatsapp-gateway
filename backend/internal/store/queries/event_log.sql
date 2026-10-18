-- name: AppendEventLog :execresult
INSERT INTO event_log (event_id, organization_id, session_id, type, payload, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- Replay (stream/gRPC ?since, GET events) skips bulk events: 'history.batch'
-- must match domain.EventHistoryBatch (pinned by TestEventLogReplaySkipsBulkEvents).
-- name: ListEventLogSinceForOrg :many
SELECT id, event_id, organization_id, session_id, type, payload, created_at
FROM event_log
WHERE organization_id = ? AND id > ? AND type <> 'history.batch'
ORDER BY id ASC
LIMIT ?;

-- name: ListEventLogSinceForSession :many
SELECT id, event_id, organization_id, session_id, type, payload, created_at
FROM event_log
WHERE organization_id = ? AND session_id = ? AND id > ? AND type <> 'history.batch'
ORDER BY id ASC
LIMIT ?;

-- name: GetEventLogByEventID :one
SELECT id, event_id, organization_id, session_id, type, payload, created_at
FROM event_log
WHERE event_id = ?;
