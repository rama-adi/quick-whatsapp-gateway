DROP TABLE history_sync_batches;
DROP TABLE history_sync_runs;
ALTER TABLE wa_sessions
  DROP COLUMN account_changed_at,
  DROP COLUMN data_account_jid,
  DROP COLUMN history_sync_days;
ALTER TABLE messages DROP COLUMN origin;
