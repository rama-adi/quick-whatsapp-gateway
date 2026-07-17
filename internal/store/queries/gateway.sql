-- name: UpsertGateway :exec
INSERT INTO gateways (id, label, status, session_count, capacity, base_url, last_seen_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE label=VALUES(label), status=VALUES(status),
	capacity=VALUES(capacity), base_url=VALUES(base_url),
	last_seen_at=VALUES(last_seen_at), updated_at=VALUES(updated_at);

-- name: GetGateway :one
SELECT id, label, status, session_count, capacity, base_url, last_seen_at, created_at, updated_at
FROM gateways
WHERE id = ? AND deleted_at IS NULL;

-- name: GatewayHeartbeat :exec
UPDATE gateways
SET last_seen_at = ?, session_count = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL;

-- name: SetGatewayStatus :exec
UPDATE gateways
SET status = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL;

-- name: ListActiveGateways :many
SELECT id, label, status, session_count, capacity, base_url, last_seen_at, created_at, updated_at
FROM gateways
WHERE status = ? AND deleted_at IS NULL
ORDER BY session_count ASC, id ASC;

-- name: PickGatewayForPlacement :one
SELECT id, label, status, session_count, capacity, base_url, last_seen_at, created_at, updated_at
FROM gateways
WHERE status = ? AND deleted_at IS NULL AND (capacity IS NULL OR session_count < capacity)
ORDER BY session_count ASC, last_seen_at DESC, id ASC
LIMIT 1;

-- name: CreateGateway :exec
INSERT INTO gateways
(id, label, notes, status, creator_kind, created_by_user_id, capacity, desired_revision, applied_revision, created_at, updated_at)
VALUES (?, ?, ?, ?, 'user', ?, ?, ?, 0, ?, ?);

-- name: ListGateways :many
SELECT id, label, status, session_count, capacity, base_url, last_seen_at, created_at, updated_at
FROM gateways WHERE deleted_at IS NULL ORDER BY created_at DESC, id DESC;

-- name: UpdateGatewayMetadata :execrows
UPDATE gateways SET label=?, notes=?, capacity=?, desired_revision=?, updated_at=? WHERE id=? AND deleted_at IS NULL;

-- name: DisableGateway :execrows
UPDATE gateways SET status='disabled', updated_at=? WHERE id=? AND deleted_at IS NULL AND status<>'disabled';

-- name: SoftDeleteGateway :execrows
UPDATE gateways SET status='disabled', deleted_at=?, updated_at=?
WHERE gateways.id=? AND deleted_at IS NULL AND status IN ('pending_enrollment','drained','disabled')
  AND NOT EXISTS (SELECT 1 FROM gateway_enrollment_tokens t WHERE t.gateway_id=gateways.id AND t.status IN ('active','redeeming'))
  AND NOT EXISTS (SELECT 1 FROM gateway_certificates c WHERE c.gateway_id=gateways.id AND c.revoked_at IS NULL);

-- name: UpdateGatewayConnectionMetadata :execrows
UPDATE gateways SET grpc_endpoint=?, software_version=?, capabilities=?, applied_revision=?, connected_at=?, updated_at=? WHERE id=? AND deleted_at IS NULL;
