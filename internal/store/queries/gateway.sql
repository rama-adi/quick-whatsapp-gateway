-- name: UpsertGateway :exec
INSERT INTO gateways (id, label, status, connection_mode, session_count, capacity, base_url, last_seen_at, created_at, updated_at)
VALUES (?, ?, ?, 'legacy', ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE label=VALUES(label), status=VALUES(status),
	connection_mode='legacy', capacity=VALUES(capacity), base_url=VALUES(base_url),
	last_seen_at=VALUES(last_seen_at), updated_at=VALUES(updated_at);

-- name: GetGateway :one
SELECT id, label, notes, status, session_count, capacity, base_url, grpc_endpoint,
       software_version, capabilities, connection_epoch, connection_mode,
       desired_lifecycle, desired_revision, applied_revision, enrolled_at,
       connected_at, last_seen_at, created_at, updated_at
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

-- name: SetGatewayDesiredLifecycle :execrows
UPDATE gateways
SET desired_lifecycle = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL
  AND enrolled_at IS NOT NULL
  AND status NOT IN ('pending_enrollment', 'disabled');

-- name: ListActiveGateways :many
SELECT id, label, notes, status, session_count, capacity, base_url, grpc_endpoint,
       software_version, capabilities, connection_epoch, connection_mode,
       desired_lifecycle, desired_revision, applied_revision, enrolled_at,
       connected_at, last_seen_at, created_at, updated_at
FROM gateways
WHERE status = ? AND deleted_at IS NULL
ORDER BY session_count ASC, id ASC;

-- name: PickGatewayForPlacement :one
SELECT id, label, notes, status, session_count, capacity, base_url, grpc_endpoint,
       software_version, capabilities, connection_epoch, connection_mode,
       desired_lifecycle, desired_revision, applied_revision, enrolled_at,
       connected_at, last_seen_at, created_at, updated_at
FROM gateways
WHERE status = ? AND (connection_mode = 'legacy' OR desired_lifecycle = 'run')
  AND deleted_at IS NULL AND (capacity IS NULL OR session_count < capacity)
ORDER BY session_count ASC, last_seen_at DESC, id ASC
LIMIT 1;

-- name: CreateGateway :exec
INSERT INTO gateways
(id, label, notes, status, creator_kind, created_by_user_id, capacity, desired_revision, applied_revision, created_at, updated_at)
VALUES (?, ?, ?, ?, 'user', ?, ?, ?, 0, ?, ?);

-- name: ListGateways :many
SELECT id, label, notes, status, session_count, capacity, base_url, grpc_endpoint,
       software_version, capabilities, connection_epoch, connection_mode,
       desired_lifecycle, desired_revision, applied_revision, enrolled_at,
       connected_at, last_seen_at, created_at, updated_at
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

-- name: GetGatewayConnectionEpochForAllocation :one
SELECT connection_epoch
FROM gateways
WHERE id = ?
  AND deleted_at IS NULL
  AND enrolled_at IS NOT NULL
  AND status NOT IN ('pending_enrollment', 'disabled')
LIMIT 1;

-- name: AllocateGatewayConnectionEpoch :execrows
UPDATE gateways
SET connection_epoch = connection_epoch + 1,
    connection_mode = 'control',
    connected_at = ?,
    last_seen_at = ?,
    base_url = COALESCE(?, base_url),
    grpc_endpoint = ?,
    software_version = ?,
    capabilities = ?,
    session_count = ?,
    status = sqlc.arg(reported_status),
    updated_at = ?
WHERE id = ?
  AND connection_epoch = ?
  AND deleted_at IS NULL
  AND enrolled_at IS NOT NULL
  AND status NOT IN ('pending_enrollment', 'disabled');

-- name: GatewayHeartbeatForEpoch :execrows
UPDATE gateways
SET last_seen_at = ?, session_count = ?,
    status = sqlc.arg(reported_status),
    updated_at = ?
WHERE id = ? AND connection_epoch = ? AND deleted_at IS NULL
  AND status NOT IN ('pending_enrollment', 'disabled');

-- name: SetGatewayStatusForEpoch :execrows
UPDATE gateways
SET status = sqlc.arg(reported_status),
    updated_at = ?
WHERE id = ? AND connection_epoch = ? AND deleted_at IS NULL
  AND status NOT IN ('pending_enrollment', 'disabled');

-- name: DisconnectGatewayForEpoch :execrows
UPDATE gateways
SET connected_at = NULL, last_seen_at = NULL, updated_at = ?
WHERE id = ? AND connection_epoch = ? AND deleted_at IS NULL
  AND status NOT IN ('pending_enrollment', 'disabled');

-- name: UpdateGatewayConnectionMetadataForEpoch :execrows
UPDATE gateways
SET grpc_endpoint = ?, software_version = ?, capabilities = ?,
    applied_revision = ?, last_seen_at = ?, updated_at = ?
WHERE id = ? AND connection_epoch = ? AND deleted_at IS NULL
  AND status NOT IN ('pending_enrollment', 'disabled');

-- name: IsGatewayConnectionCurrent :one
SELECT EXISTS(
  SELECT 1 FROM gateways
  WHERE id = ? AND connection_epoch = ? AND deleted_at IS NULL
    AND status NOT IN ('pending_enrollment', 'disabled')
) AS is_current;

-- name: GetAcceptedGatewayDesiredLifecycle :one
SELECT desired_lifecycle
FROM gateways
WHERE id = ? AND connection_epoch = ? AND deleted_at IS NULL
  AND status NOT IN ('pending_enrollment', 'disabled')
LIMIT 1;
