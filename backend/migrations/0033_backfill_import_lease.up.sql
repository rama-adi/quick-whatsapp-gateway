-- Backup imports run as in-process jobs. A running row now names its owner
-- process and carries a heartbeat and a lease; the owner renews the lease
-- while it works. The API maintenance loop (at boot and periodically) marks a
-- running row whose lease expired as 'interrupted', so an API restart or crash
-- never leaves an import running forever. This replaces the implied
-- "running and created less than 60 minutes ago" rule.
ALTER TABLE backfill_imports
  MODIFY COLUMN status ENUM('running','succeeded','failed','interrupted') NOT NULL DEFAULT 'running',
  ADD COLUMN owner        VARCHAR(255) NULL AFTER status,
  ADD COLUMN heartbeat_at BIGINT NULL AFTER owner,
  ADD COLUMN lease_until  BIGINT NULL AFTER heartbeat_at;

-- Existing running rows keep the old 60-minute rule as their lease.
UPDATE backfill_imports
SET heartbeat_at = created_at, lease_until = created_at + 3600000
WHERE status = 'running';

ALTER TABLE backfill_imports
  ADD KEY idx_backfill_running (status, lease_until),
  ADD CONSTRAINT chk_backfill_running_lease CHECK ((status = 'running') = (lease_until IS NOT NULL));
