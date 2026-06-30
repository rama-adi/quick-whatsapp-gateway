-- name: InsertOutbox :exec
INSERT INTO outbox
(id, organization_id, session_id, idempotency_key, payload, status, attempts,
 wa_message_id, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetOutbox :one
SELECT id, organization_id, session_id, idempotency_key, payload, status,
	attempts, wa_message_id, error, created_at, updated_at
FROM outbox
WHERE id = ?;

-- name: GetOutboxByIdempotency :one
SELECT id, organization_id, session_id, idempotency_key, payload, status,
	attempts, wa_message_id, error, created_at, updated_at
FROM outbox
WHERE organization_id = ? AND idempotency_key = ?;

-- name: UpdateOutboxStatus :execrows
UPDATE outbox
SET status = ?, wa_message_id = ?, error = ?, updated_at = ?
WHERE id = ?;

-- name: UpdateOutboxStatusAndStripMedia :execrows
UPDATE outbox
SET status = ?, wa_message_id = ?, error = ?, updated_at = ?,
	payload = JSON_REMOVE(payload, '$.media.data')
WHERE id = ?;

-- name: ClaimQueuedOutbox :exec
UPDATE outbox
SET status = ?, attempts = attempts + 1, updated_at = ?
WHERE status = ?
ORDER BY created_at ASC
LIMIT ?;

-- name: ListClaimedOutbox :many
SELECT id, organization_id, session_id, idempotency_key, payload, status,
	attempts, wa_message_id, error, created_at, updated_at
FROM outbox
WHERE status = ? AND updated_at = ?
ORDER BY created_at ASC
LIMIT ?;
