-- name: IssueGatewayEnrollmentToken :exec
INSERT INTO gateway_enrollment_tokens
(id, gateway_id, token_hash, token_prefix, status, attempt_count, max_attempts, expires_at, created_by_user_id, created_at, updated_at)
VALUES (?, ?, ?, ?, 'active', 0, ?, ?, ?, ?, ?);

-- name: GetGatewayEnrollmentTokenForUpdate :one
SELECT * FROM gateway_enrollment_tokens WHERE id = ? FOR UPDATE;

-- name: RevokeGatewayEnrollmentToken :execrows
UPDATE gateway_enrollment_tokens SET status='revoked', revoked_at=?, updated_at=?
  , redemption_nonce=NULL, csr_sha256=NULL, redeeming_at=NULL, lease_expires_at=NULL
WHERE id=? AND status IN ('active','redeeming');

-- name: LockGatewayEnrollmentToken :execrows
UPDATE gateway_enrollment_tokens SET status='locked', updated_at=?
  , redemption_nonce=NULL, csr_sha256=NULL, redeeming_at=NULL, lease_expires_at=NULL
WHERE id=? AND status IN ('active','redeeming');

-- name: BeginGatewayEnrollment :execrows
UPDATE gateway_enrollment_tokens
SET status='redeeming', redemption_nonce=?, csr_sha256=?, redeeming_at=?, lease_expires_at=?,
    attempt_count=attempt_count+1, updated_at=?
WHERE id=? AND expires_at>? AND attempt_count<max_attempts
  AND (status='active' OR (status='redeeming' AND lease_expires_at<=? AND csr_sha256=?));

-- name: FinalizeGatewayEnrollment :execrows
UPDATE gateway_enrollment_tokens
SET status='consumed', consumed_at=?, redemption_nonce=NULL, csr_sha256=NULL,
    redeeming_at=NULL, lease_expires_at=NULL, updated_at=?
WHERE id=? AND status='redeeming' AND redemption_nonce=? AND csr_sha256=? AND lease_expires_at>?;

-- name: ReleaseGatewayEnrollment :execrows
UPDATE gateway_enrollment_tokens
SET status='active', redemption_nonce=NULL, csr_sha256=NULL, redeeming_at=NULL,
    lease_expires_at=NULL, updated_at=?
WHERE id=? AND status='redeeming' AND redemption_nonce=? AND csr_sha256=? AND lease_expires_at>?;

-- name: InsertGatewayCertificate :exec
INSERT INTO gateway_certificates
(id, gateway_id, authority_id, serial_number, certificate_pem, certificate_fingerprint, not_before, not_after, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListGatewayCertificates :many
SELECT * FROM gateway_certificates WHERE gateway_id=? ORDER BY created_at DESC, id DESC;

-- name: RevokeGatewayCertificate :execrows
UPDATE gateway_certificates SET revoked_at=?, revocation_reason=?
WHERE id=? AND revoked_at IS NULL;

-- name: AppendAuditEvent :exec
INSERT INTO audit_events
(id, organization_id, actor_type, actor_id, action, resource_type, resource_id, outcome, request_id, source_ip, metadata, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListAuditEventsByResource :many
SELECT * FROM audit_events
WHERE resource_type=? AND resource_id=? AND created_at<?
ORDER BY created_at DESC, id DESC LIMIT ?;
