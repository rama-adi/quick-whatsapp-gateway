ALTER TABLE outbox
  DROP CHECK chk_outbox_acknowledged,
  DROP CHECK chk_outbox_lease,
  DROP CHECK chk_outbox_terminal,
  DROP KEY idx_outbox_lease;
UPDATE outbox SET status = 'queued' WHERE status = 'acknowledged';
UPDATE outbox SET updated_at = lease_until - 360000 WHERE status = 'sending' AND lease_until IS NOT NULL;
ALTER TABLE outbox
  DROP COLUMN lease_until,
  DROP COLUMN claim_token,
  DROP COLUMN projection_attempts,
  DROP COLUMN deferrals,
  RENAME COLUMN dispatch_attempts TO attempts,
  MODIFY COLUMN status ENUM('queued','sending','sent','failed','unknown') NOT NULL DEFAULT 'queued';
