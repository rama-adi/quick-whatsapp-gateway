UPDATE history_sync_runs SET status = 'failed' WHERE status = 'abandoned';
ALTER TABLE history_sync_runs DROP KEY idx_hsr_expected, DROP COLUMN expected_by,
  MODIFY COLUMN status ENUM('running','completed','failed') NOT NULL DEFAULT 'running';
