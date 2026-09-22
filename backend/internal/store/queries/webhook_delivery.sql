-- name: EnqueueWebhookDelivery :execresult
INSERT INTO webhook_deliveries
(webhook_id, event_id, status, attempts, next_retry_at, created_at)
VALUES (?, ?, ?, 0, ?, ?)
ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id);

-- name: ClaimDueWebhookDeliveries :many
SELECT id, webhook_id, event_id, status, attempts, response_code, next_retry_at, last_error, created_at
FROM webhook_deliveries
WHERE status IN (?, ?) AND next_retry_at IS NOT NULL AND next_retry_at <= ?
ORDER BY next_retry_at ASC
LIMIT ?
FOR UPDATE SKIP LOCKED;

-- name: LeaseWebhookDelivery :execrows
UPDATE webhook_deliveries
SET next_retry_at = ?
WHERE id = ?;

-- name: MarkWebhookDeliveryDelivered :execrows
UPDATE webhook_deliveries
SET status = ?, attempts = attempts + 1, response_code = ?, next_retry_at = NULL, last_error = NULL
WHERE id = ?;

-- name: MarkWebhookDeliveryFailed :execrows
UPDATE webhook_deliveries
SET status = ?, attempts = attempts + 1, response_code = ?, last_error = ?, next_retry_at = ?
WHERE id = ?;

-- name: MarkWebhookDeliveryDead :execrows
UPDATE webhook_deliveries
SET status = ?, attempts = attempts + 1, response_code = ?, last_error = ?, next_retry_at = NULL
WHERE id = ?;

-- name: WebhookDeliveryTerminalExists :one
SELECT EXISTS (
	SELECT 1
	FROM webhook_deliveries
	WHERE webhook_id = ? AND event_id = ? AND status IN (?, ?)
	LIMIT 1
);
