-- Authoritative API-owned desired-state assignments. wa_sessions.gateway_id is
-- retained during the incremental transport migration, while this table is the
-- control stream's ownership and split-brain fence.
CREATE TABLE gateway_session_assignments (
  session_id        VARCHAR(64) PRIMARY KEY,
  gateway_id        VARCHAR(64) NOT NULL,
  assignment_epoch  BIGINT UNSIGNED NOT NULL,
  created_at        BIGINT NOT NULL,
  updated_at        BIGINT NOT NULL,
  CONSTRAINT fk_gateway_session_assignment_session
    FOREIGN KEY (session_id) REFERENCES wa_sessions(id) ON DELETE CASCADE,
  CONSTRAINT fk_gateway_session_assignment_gateway
    FOREIGN KEY (gateway_id) REFERENCES gateways(id) ON DELETE RESTRICT,
  CONSTRAINT chk_gateway_session_assignment_epoch CHECK (assignment_epoch > 0),
  KEY idx_gateway_session_assignments_gateway (gateway_id, session_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Revision zero belonged to the pre-snapshot registry protocol. Every gateway
-- therefore has a nonzero initial desired snapshot revision, including one
-- with no assigned sessions.
UPDATE gateways
SET desired_revision = 1
WHERE desired_revision = 0;

ALTER TABLE gateways
  ADD COLUMN reconciliation_status ENUM('pending','healthy','degraded') NOT NULL DEFAULT 'pending',
  ADD COLUMN keystore_present TINYINT(1) NULL,
  ADD COLUMN keystore_bytes BIGINT NULL,
  ADD COLUMN keystore_integrity ENUM('healthy','missing','corrupt') NULL,
  ADD COLUMN keystore_checked_at BIGINT NULL,
  ADD KEY idx_gateways_reconciliation (connection_mode, reconciliation_status, desired_revision, applied_revision);

CREATE TABLE gateway_reconciliation_results (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  gateway_id VARCHAR(64) NOT NULL,
  device_jid VARCHAR(255) NULL,
  session_id VARCHAR(64) NULL,
  assignment_epoch BIGINT UNSIGNED NOT NULL DEFAULT 0,
  status ENUM('applied','keystore_missing','keystore_corrupt','unexpected_local_device') NOT NULL,
  desired_revision BIGINT UNSIGNED NOT NULL,
  updated_at BIGINT NOT NULL,
  CONSTRAINT fk_gateway_reconciliation_results_gateway FOREIGN KEY (gateway_id) REFERENCES gateways(id) ON DELETE CASCADE,
  CONSTRAINT fk_gateway_reconciliation_results_session FOREIGN KEY (session_id) REFERENCES wa_sessions(id) ON DELETE CASCADE,
  CONSTRAINT chk_gateway_reconciliation_result_shape CHECK (
    (status='unexpected_local_device' AND session_id IS NULL AND assignment_epoch=0) OR
    (status<>'unexpected_local_device' AND session_id IS NOT NULL AND assignment_epoch>0)
  ),
  UNIQUE KEY uq_gateway_reconciliation_assignment (gateway_id, session_id),
  UNIQUE KEY uq_gateway_reconciliation_device (gateway_id, device_jid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

INSERT INTO gateway_session_assignments
  (session_id, gateway_id, assignment_epoch, created_at, updated_at)
SELECT id, gateway_id, 1, created_at, updated_at
FROM wa_sessions;
