-- Gateway journal entries the API can never accept (an undecodable envelope,
-- or a payload whose media descriptor cannot be parsed). The API records the
-- verdict here and acknowledges the entry with the rest of its batch, so the
-- gateway's strict-FIFO journal advances past it instead of replaying the same
-- batch forever. The payload is not stored: it can carry media keys. The
-- gateway keeps undecodable entries in its local journal_quarantine table.
CREATE TABLE gateway_event_rejections (
  gateway_event_id VARCHAR(128) NOT NULL PRIMARY KEY,
  gateway_id       VARCHAR(64) NOT NULL,
  connection_epoch BIGINT UNSIGNED NOT NULL,
  journal_sequence BIGINT UNSIGNED NOT NULL,
  session_id       VARCHAR(64) NOT NULL,
  organization_id  VARCHAR(64) NOT NULL,
  event_type       VARCHAR(64) NOT NULL,
  reason           TEXT NOT NULL,
  payload_bytes    INT UNSIGNED NOT NULL,
  rejected_at      BIGINT NOT NULL,
  KEY idx_gateway_event_rejections_session (session_id, rejected_at),
  KEY idx_gateway_event_rejections_rejected (rejected_at)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
