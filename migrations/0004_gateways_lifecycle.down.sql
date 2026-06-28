-- Reverse of 0004_gateways_lifecycle.up.sql.
DROP INDEX idx_gateways_status_seen ON gateways;
ALTER TABLE gateways
  DROP COLUMN capacity,
  DROP COLUMN session_count,
  DROP COLUMN status;
