-- A running history-sync run no longer depends on the gateway's final
-- history.sync event to end. Every started/progress event and every applied
-- batch moves expected_by forward; the API's maintenance loop marks a run whose
-- expected_by passed as 'abandoned' (terminal, like completed and failed).
-- Appending an ENUM member and adding a NULL column are metadata-only changes.
ALTER TABLE history_sync_runs
  MODIFY COLUMN status ENUM('running','completed','failed','abandoned') NOT NULL DEFAULT 'running',
  ADD COLUMN expected_by BIGINT NULL AFTER finished_at;

UPDATE history_sync_runs SET expected_by = updated_at + 93600000 WHERE status = 'running';

ALTER TABLE history_sync_runs ADD KEY idx_hsr_expected (status, expected_by);
