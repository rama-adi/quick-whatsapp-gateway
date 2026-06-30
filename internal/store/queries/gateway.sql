-- name: UpsertGateway :exec
INSERT INTO gateways (id, label, status, session_count, capacity, base_url, last_seen_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE label=VALUES(label), status=VALUES(status),
	capacity=VALUES(capacity), base_url=VALUES(base_url),
	last_seen_at=VALUES(last_seen_at), updated_at=VALUES(updated_at);

-- name: GetGateway :one
SELECT id, label, status, session_count, capacity, base_url, last_seen_at, created_at, updated_at
FROM gateways
WHERE id = ?;

-- name: GatewayHeartbeat :exec
UPDATE gateways
SET last_seen_at = ?, session_count = ?, updated_at = ?
WHERE id = ?;

-- name: SetGatewayStatus :exec
UPDATE gateways
SET status = ?, updated_at = ?
WHERE id = ?;

-- name: ListActiveGateways :many
SELECT id, label, status, session_count, capacity, base_url, last_seen_at, created_at, updated_at
FROM gateways
WHERE status = ?
ORDER BY session_count ASC, id ASC;

-- name: PickGatewayForPlacement :one
SELECT id, label, status, session_count, capacity, base_url, last_seen_at, created_at, updated_at
FROM gateways
WHERE status = ? AND (capacity IS NULL OR session_count < capacity)
ORDER BY session_count ASC, last_seen_at DESC, id ASC
LIMIT 1;
