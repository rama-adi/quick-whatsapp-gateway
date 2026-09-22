-- API-owned durable acknowledgement ledger for at-least-once gateway event delivery.
CREATE TABLE gateway_ingested_events (
  gateway_event_id VARCHAR(128) PRIMARY KEY,
  gateway_id VARCHAR(64) NOT NULL,
  connection_epoch BIGINT UNSIGNED NOT NULL,
  session_id VARCHAR(64) NOT NULL,
  assignment_epoch BIGINT UNSIGNED NOT NULL,
  organization_id VARCHAR(64) NOT NULL,
  event_log_id VARCHAR(64) NOT NULL,
  committed_at BIGINT NOT NULL,
  UNIQUE KEY uq_gateway_ingested_event_log (event_log_id),
  KEY idx_gateway_ingested_session (gateway_id, session_id),
  CONSTRAINT fk_gateway_ingested_gateway FOREIGN KEY (gateway_id) REFERENCES gateways(id) ON DELETE RESTRICT,
  CONSTRAINT fk_gateway_ingested_session FOREIGN KEY (session_id) REFERENCES wa_sessions(id) ON DELETE RESTRICT,
  CONSTRAINT chk_gateway_ingested_epochs CHECK (connection_epoch > 0 AND assignment_epoch > 0)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
