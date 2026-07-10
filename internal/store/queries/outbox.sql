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

-- name: ClaimOutboxByID :execrows
UPDATE outbox
SET status = sqlc.arg(claimed_status), attempts = attempts + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND (
    status IN (sqlc.arg(queued_status), sqlc.arg(failed_status))
    OR (status = sqlc.arg(sending_status) AND updated_at <= sqlc.arg(stale_before))
  );

-- name: SelectQueuedOutboxForClaim :many
SELECT id, organization_id, session_id, idempotency_key, payload, status,
	attempts, wa_message_id, error, created_at, updated_at
FROM outbox
WHERE status = sqlc.arg(queued_status)
  AND (sqlc.arg(session_filter) = '' OR session_id = sqlc.arg(session_filter))
ORDER BY created_at ASC, id ASC
LIMIT ?
FOR UPDATE SKIP LOCKED;
