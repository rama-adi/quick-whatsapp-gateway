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
       connected_at, last_seen_at, created_at, updated_at, reconciliation_status,
       keystore_present, keystore_bytes, keystore_integrity, keystore_checked_at
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
SET desired_lifecycle = ?, desired_revision = desired_revision + 1, updated_at = ?
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
WHERE status = ? AND (
  connection_mode = 'legacy' OR
  (connection_mode = 'control' AND desired_lifecycle = 'run'
   AND reconciliation_status = 'healthy' AND applied_revision = desired_revision)
)
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
       connected_at, last_seen_at, created_at, updated_at, reconciliation_status,
       keystore_present, keystore_bytes, keystore_integrity, keystore_checked_at
FROM gateways WHERE deleted_at IS NULL ORDER BY created_at DESC, id DESC;

-- name: ListGatewayReconciliationResults :many
SELECT device_jid, session_id, assignment_epoch, status, desired_revision, updated_at
FROM gateway_reconciliation_results
WHERE gateway_id = ?
ORDER BY device_jid ASC;

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
SELECT desired_lifecycle, desired_revision
FROM gateways
WHERE id = ? AND connection_epoch = ? AND deleted_at IS NULL
  AND status NOT IN ('pending_enrollment', 'disabled')
LIMIT 1;

-- name: ListGatewayDesiredStateAssignments :many
SELECT a.session_id, s.organization_id, s.wa_jid, s.status, a.assignment_epoch,
       s.auto_read, s.presence_typing, s.rate_per_min, s.rate_per_hour
FROM gateway_session_assignments AS a
JOIN wa_sessions AS s ON s.id = a.session_id
JOIN gateways AS g ON g.id = a.gateway_id
WHERE a.gateway_id = ? AND g.connection_epoch = ? AND g.desired_revision = ? AND g.deleted_at IS NULL
  AND g.status NOT IN ('pending_enrollment', 'disabled')
ORDER BY a.session_id ASC;

-- name: AcknowledgeGatewayDesiredStateForEpoch :execrows
UPDATE gateways
SET applied_revision = ?, updated_at = ?
WHERE id = ? AND connection_epoch = ? AND desired_revision = ? AND deleted_at IS NULL
  AND status NOT IN ('pending_enrollment', 'disabled')
  AND applied_revision <= ?;

-- name: ResolveSessionEngineTarget :one
SELECT s.id AS session_id, s.organization_id, a.gateway_id, a.assignment_epoch,
       g.grpc_endpoint, g.connection_epoch
FROM wa_sessions AS s
JOIN gateway_session_assignments AS a ON a.session_id=s.id
JOIN gateways AS g ON g.id=a.gateway_id
WHERE s.id=? AND s.organization_id=? AND g.deleted_at IS NULL
  AND g.connection_mode='control' AND g.status='active'
  AND g.desired_lifecycle='run' AND g.reconciliation_status='healthy'
  AND g.desired_revision=g.applied_revision
  AND g.connected_at IS NOT NULL AND g.grpc_endpoint IS NOT NULL
LIMIT 1;
