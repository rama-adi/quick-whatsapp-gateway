-- name: InsertOutbox :exec
INSERT INTO outbox
(id, organization_id, session_id, idempotency_key, payload, status, attempts,
 next_attempt_at, wa_message_id, error, terminal_at, created_at, updated_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetOutbox :one
SELECT id, organization_id, session_id, idempotency_key, payload, status,
	attempts, next_attempt_at, wa_message_id, error, terminal_at, created_at, updated_at,
	expires_at, acknowledged_at, effect_possible
FROM outbox
WHERE id = ?;

-- name: GetOutboxByIdempotency :one
SELECT id, organization_id, session_id, idempotency_key, payload, status,
	attempts, next_attempt_at, wa_message_id, error, terminal_at, created_at, updated_at,
	expires_at, acknowledged_at, effect_possible
FROM outbox
WHERE organization_id = ? AND idempotency_key = ?;

-- name: UpdateOutboxStatus :execrows
UPDATE outbox
SET status = ?, wa_message_id = ?, error = ?, updated_at = ?
WHERE id = ?;

-- name: RescheduleOutbox :execrows
UPDATE outbox
SET status = sqlc.arg(queued_status), error = ?, next_attempt_at = ?,
    updated_at = ?
WHERE id = ? AND status = sqlc.arg(sending_status) AND updated_at = sqlc.arg(owner_stamp) AND terminal_at IS NULL;

-- name: CompleteOutbox :execrows
UPDATE outbox
SET status = sqlc.arg(final_status), wa_message_id = ?, error = ?,
    terminal_at = ?, updated_at = ?
WHERE id = ? AND status = sqlc.arg(sending_status);

-- name: MarkOutboxDispatchPossible :execrows
UPDATE outbox SET effect_possible = TRUE
WHERE id = ? AND status = 'sending' AND updated_at = sqlc.arg(owner_stamp) AND terminal_at IS NULL;

-- name: ClearOutboxDispatchPossible :execrows
UPDATE outbox SET effect_possible = FALSE
WHERE id = ? AND status = 'sending' AND updated_at = sqlc.arg(owner_stamp)
  AND terminal_at IS NULL AND acknowledged_at IS NULL;

-- name: RecordOutboxAcknowledgement :execrows
UPDATE outbox SET wa_message_id = ?, acknowledged_at = ?
WHERE id = ? AND status = 'sending' AND updated_at = sqlc.arg(owner_stamp) AND terminal_at IS NULL;

-- name: CompleteOwnedOutbox :execrows
UPDATE outbox SET status = sqlc.arg(final_status), wa_message_id = COALESCE(sqlc.narg(wa_message_id), wa_message_id),
  error = sqlc.narg(error), terminal_at = sqlc.arg(terminal_at), updated_at = sqlc.arg(updated_at),
  payload = CASE WHEN sqlc.arg(final_status) = 'sent' THEN
    JSON_REMOVE(payload, '$.media.data', '$.medias[0].data', '$.medias[1].data', '$.medias[2].data', '$.medias[3].data', '$.medias[4].data', '$.medias[5].data', '$.medias[6].data', '$.medias[7].data', '$.medias[8].data', '$.medias[9].data')
    ELSE payload END
WHERE id = sqlc.arg(id) AND status = 'sending' AND updated_at = sqlc.arg(owner_stamp) AND terminal_at IS NULL;

-- name: UpdateOutboxStatusAndStripMedia :execrows
UPDATE outbox
SET status = ?, wa_message_id = ?, error = ?, updated_at = ?,
	payload = JSON_REMOVE(payload, '$.media.data', '$.medias[0].data', '$.medias[1].data', '$.medias[2].data', '$.medias[3].data', '$.medias[4].data', '$.medias[5].data', '$.medias[6].data', '$.medias[7].data', '$.medias[8].data', '$.medias[9].data'),
	terminal_at = ?
WHERE id = ?;

-- name: ClaimOutboxByID :execrows
UPDATE outbox
SET status = sqlc.arg(claimed_status), attempts = attempts + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND terminal_at IS NULL
  AND (
    status IN (sqlc.arg(queued_status), sqlc.arg(failed_status))
    OR (status = sqlc.arg(sending_status) AND updated_at <= sqlc.arg(stale_before))
  );

-- name: SelectQueuedOutboxForClaim :many
SELECT id, organization_id, session_id, idempotency_key, payload, status,
	attempts, next_attempt_at, wa_message_id, error, terminal_at, created_at, updated_at,
	expires_at, acknowledged_at, effect_possible
FROM outbox
WHERE status = sqlc.arg(queued_status)
  AND terminal_at IS NULL
  AND (sqlc.arg(session_filter) = '' OR session_id = sqlc.arg(session_filter))
ORDER BY created_at ASC, id ASC
LIMIT ?
FOR UPDATE SKIP LOCKED;

-- SelectDueOutboxForClaim returns retryable work: fresh commands whose
-- next_attempt_at is due, plus stale 'sending' rows whose dispatch lease has
-- expired. Ordering is by scheduled attempt time so the oldest-due fire first.
-- name: SelectDueOutboxForClaim :many
SELECT id, organization_id, session_id, idempotency_key, payload, status,
	attempts, next_attempt_at, wa_message_id, error, terminal_at, created_at, updated_at,
	expires_at, acknowledged_at, effect_possible
FROM outbox
WHERE terminal_at IS NULL AND (
  (
    (status = sqlc.arg(queued_status) OR status = sqlc.arg(failed_status))
    AND (next_attempt_at <= sqlc.arg(due_before)
      OR (expires_at IS NOT NULL AND expires_at <= sqlc.arg(expiry_before)))
  )
  OR (status = sqlc.arg(sending_status) AND updated_at <= sqlc.arg(stale_before))
)
ORDER BY next_attempt_at ASC, created_at ASC, id ASC
LIMIT ?
FOR UPDATE SKIP LOCKED;
