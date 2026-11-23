-- name: InsertOutbox :exec
INSERT INTO outbox
(id, organization_id, session_id, idempotency_key, payload, status, dispatch_attempts,
 next_attempt_at, wa_message_id, error, terminal_at, created_at, updated_at, expires_at,
 claim_token, lease_until)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetOutbox :one
SELECT id, organization_id, session_id, idempotency_key, payload, status,
	dispatch_attempts, deferrals, projection_attempts, next_attempt_at, wa_message_id, error,
	terminal_at, created_at, updated_at, expires_at, acknowledged_at, effect_possible,
	claim_token, lease_until
FROM outbox
WHERE id = ?;

-- name: GetOutboxByIdempotency :one
SELECT id, organization_id, session_id, idempotency_key, payload, status,
	dispatch_attempts, deferrals, projection_attempts, next_attempt_at, wa_message_id, error,
	terminal_at, created_at, updated_at, expires_at, acknowledged_at, effect_possible,
	claim_token, lease_until
FROM outbox
WHERE organization_id = ? AND idempotency_key = ?;

-- SelectDueOutboxForClaim returns claimable work: queued commands and
-- acknowledged projections that are due (or past their expiry), plus leased
-- ('sending') commands whose lease expired.
-- name: SelectDueOutboxForClaim :many
SELECT id, organization_id, session_id, idempotency_key, payload, status,
	dispatch_attempts, deferrals, projection_attempts, next_attempt_at, wa_message_id, error,
	terminal_at, created_at, updated_at, expires_at, acknowledged_at, effect_possible,
	claim_token, lease_until
FROM outbox
WHERE terminal_at IS NULL AND (
  (status IN ('queued', 'acknowledged')
    AND (next_attempt_at <= sqlc.arg(now) OR (expires_at IS NOT NULL AND expires_at <= sqlc.arg(expiry_before))))
  OR (status = 'sending' AND lease_until <= sqlc.arg(lease_before))
)
ORDER BY next_attempt_at ASC, created_at ASC, id ASC
LIMIT ?
FOR UPDATE SKIP LOCKED;

-- name: LeaseOutbox :execrows
UPDATE outbox
SET status = 'sending', claim_token = sqlc.arg(claim_token), lease_until = sqlc.arg(lease_until),
    updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND terminal_at IS NULL AND (
  status IN ('queued', 'acknowledged') OR (status = 'sending' AND lease_until <= sqlc.arg(lease_before))
);

-- name: RenewOutboxLease :execrows
UPDATE outbox SET lease_until = ?
WHERE id = ? AND claim_token = ? AND status = 'sending';

-- MarkOutboxDispatch runs before every engine dispatch: it records that a
-- remote effect may happen, counts the dispatch attempt and renews the lease.
-- name: MarkOutboxDispatch :execrows
UPDATE outbox SET effect_possible = TRUE, dispatch_attempts = dispatch_attempts + 1, lease_until = ?
WHERE id = ? AND claim_token = ? AND status = 'sending' AND acknowledged_at IS NULL;

-- name: ClearOutboxDispatchPossible :execrows
UPDATE outbox SET effect_possible = FALSE
WHERE id = ? AND claim_token = ? AND status = 'sending' AND acknowledged_at IS NULL;

-- name: RecordOutboxAcknowledgement :execrows
UPDATE outbox SET wa_message_id = ?, acknowledged_at = ?
WHERE id = ? AND claim_token = ? AND status = 'sending';

-- DeferOutbox returns a command to queued without a dispatch attempt
-- (rate limit, or an infrastructure check that failed before dispatch).
-- name: DeferOutbox :execrows
UPDATE outbox
SET status = 'queued', deferrals = deferrals + 1, error = ?, next_attempt_at = ?, updated_at = ?,
    claim_token = NULL, lease_until = NULL
WHERE id = ? AND claim_token = ? AND status = 'sending' AND acknowledged_at IS NULL;

-- RetryOutbox returns a command to queued after an ambiguous dispatch attempt.
-- name: RetryOutbox :execrows
UPDATE outbox
SET status = 'queued', error = ?, next_attempt_at = ?, updated_at = ?,
    claim_token = NULL, lease_until = NULL
WHERE id = ? AND claim_token = ? AND status = 'sending' AND acknowledged_at IS NULL;

-- DeferOutboxProjection parks an acknowledged send whose projection failed.
-- name: DeferOutboxProjection :execrows
UPDATE outbox
SET status = 'acknowledged', projection_attempts = projection_attempts + 1, error = ?,
    next_attempt_at = ?, updated_at = ?, claim_token = NULL, lease_until = NULL
WHERE id = ? AND claim_token = ? AND status = 'sending' AND acknowledged_at IS NOT NULL;

-- name: CompleteOwnedOutbox :execrows
UPDATE outbox SET status = sqlc.arg(final_status), wa_message_id = COALESCE(sqlc.narg(wa_message_id), wa_message_id),
  error = sqlc.narg(error), terminal_at = sqlc.arg(terminal_at), updated_at = sqlc.arg(updated_at),
  claim_token = NULL, lease_until = NULL,
  payload = CASE WHEN sqlc.arg(final_status) = 'sent' THEN
    JSON_REMOVE(payload, '$.media.data', '$.medias[0].data', '$.medias[1].data', '$.medias[2].data', '$.medias[3].data', '$.medias[4].data', '$.medias[5].data', '$.medias[6].data', '$.medias[7].data', '$.medias[8].data', '$.medias[9].data')
    ELSE payload END
WHERE id = sqlc.arg(id) AND claim_token = sqlc.arg(claim_token) AND status = 'sending' AND terminal_at IS NULL;
