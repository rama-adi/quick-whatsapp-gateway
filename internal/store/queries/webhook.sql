-- name: CreateWebhook :exec
INSERT INTO webhooks
(id, organization_id, session_id, url, events, hmac_secret, custom_headers,
 retry_policy, active, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, NULLIF(sqlc.arg(custom_headers), ''), ?, ?, ?, ?);

-- name: GetWebhook :one
SELECT id, organization_id, session_id, url, events, hmac_secret, COALESCE(custom_headers, '') AS custom_headers,
	retry_policy, active, created_at, updated_at
FROM webhooks
WHERE id = ?;

-- name: ListWebhooksByOrg :many
SELECT id, organization_id, session_id, url, events, hmac_secret, COALESCE(custom_headers, '') AS custom_headers,
	retry_policy, active, created_at, updated_at
FROM webhooks
WHERE organization_id = ?
ORDER BY created_at DESC;

-- name: ListActiveWebhooksForEvent :many
SELECT id, organization_id, session_id, url, events, hmac_secret, COALESCE(custom_headers, '') AS custom_headers,
	retry_policy, active, created_at, updated_at
FROM webhooks
WHERE organization_id = ? AND active = 1 AND (session_id IS NULL OR session_id = ?)
ORDER BY created_at DESC;

-- name: UpdateWebhook :execrows
UPDATE webhooks
SET session_id = ?, url = ?, events = ?, hmac_secret = ?, custom_headers = NULLIF(sqlc.arg(custom_headers), ''),
	retry_policy = ?, active = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteWebhook :execrows
DELETE FROM webhooks
WHERE id = ?;
