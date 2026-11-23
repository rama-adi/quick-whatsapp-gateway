-- name: EnqueueWebhookDelivery :execresult
INSERT INTO webhook_deliveries
(webhook_id, event_id, status, attempts, next_retry_at, created_at)
VALUES (?, ?, ?, 0, ?, ?)
ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id);

-- ClaimDueWebhookDeliveries selects due work: pending/failed rows whose
-- next_retry_at passed, plus delivering rows whose lease expired (a worker
-- crashed mid-attempt). The id tie-breaker makes the order deterministic.
-- name: ClaimDueWebhookDeliveries :many
SELECT id, webhook_id, event_id, status, attempts, response_code, next_retry_at, last_error, created_at
FROM webhook_deliveries
WHERE (status IN ('pending', 'failed') AND next_retry_at IS NOT NULL AND next_retry_at <= sqlc.arg(now))
   OR (status = 'delivering' AND lease_until <= sqlc.arg(now))
ORDER BY next_retry_at ASC, id ASC
LIMIT ?
FOR UPDATE SKIP LOCKED;

-- The attempt is counted when it is claimed, so a worker that crashes
-- mid-attempt still uses one.
-- name: LeaseWebhookDelivery :execrows
UPDATE webhook_deliveries
SET status = 'delivering', claim_token = ?, lease_until = ?, attempts = attempts + 1
WHERE id = ?;

-- name: MarkWebhookDeliveryDelivered :execrows
UPDATE webhook_deliveries
SET status = 'delivered', response_code = ?, next_retry_at = NULL, last_error = NULL,
    claim_token = NULL, lease_until = NULL
WHERE id = ? AND claim_token = ? AND status = 'delivering';

-- name: MarkWebhookDeliveryFailed :execrows
UPDATE webhook_deliveries
SET status = 'failed', response_code = ?, last_error = ?, next_retry_at = ?,
    claim_token = NULL, lease_until = NULL
WHERE id = ? AND claim_token = ? AND status = 'delivering';

-- name: MarkWebhookDeliveryDead :execrows
UPDATE webhook_deliveries
SET status = 'dead', response_code = ?, last_error = ?, next_retry_at = NULL,
    claim_token = NULL, lease_until = NULL
WHERE id = ? AND claim_token = ? AND status = 'delivering';

-- name: WebhookDeliveryTerminalExists :one
SELECT EXISTS (
	SELECT 1
	FROM webhook_deliveries
	WHERE webhook_id = ? AND event_id = ? AND status IN (?, ?)
	LIMIT 1
);
