ALTER TABLE gateway_ingested_events
  DROP KEY idx_gateway_ingested_work,
  DROP COLUMN completed_at,
  DROP COLUMN lease_until,
  DROP COLUMN claimed_by;
