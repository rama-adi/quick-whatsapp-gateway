ALTER TABLE backfill_imports DROP CHECK chk_backfill_running_lease, DROP KEY idx_backfill_running;
UPDATE backfill_imports SET status = 'failed' WHERE status = 'interrupted';
ALTER TABLE backfill_imports
  DROP COLUMN lease_until, DROP COLUMN heartbeat_at, DROP COLUMN owner,
  MODIFY COLUMN status ENUM('running','succeeded','failed') NOT NULL DEFAULT 'running';
