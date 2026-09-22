-- name: GetAPIKeyByHash :one
SELECT id, name, `key`, reference_id, enabled, expires_at, permissions, created_at
FROM apikey
WHERE `key` = ?;

-- name: GetAPIKeyByID :one
SELECT id, name, `key`, reference_id, enabled, expires_at, permissions, created_at
FROM apikey
WHERE id = ?;

-- name: TouchAPIKeyLastRequest :exec
UPDATE apikey
SET last_request = ?
WHERE id = ?;
