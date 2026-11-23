ALTER TABLE gateway_ingested_events
  DROP COLUMN journal_sequence,
  ADD COLUMN claimed_by   VARCHAR(64) NULL,
  ADD COLUMN lease_until  BIGINT NULL,
  ADD COLUMN completed_at BIGINT NULL;
UPDATE gateway_ingested_events SET completed_at = committed_at;
UPDATE gateway_ingested_events i JOIN event_fanout f ON f.event_id = i.event_log_id
SET i.completed_at = NULL
WHERE f.status IN ('pending','leased');
ALTER TABLE gateway_ingested_events ADD KEY idx_gateway_ingested_work (completed_at, lease_until);
DROP TABLE event_fanout;
DROP TABLE session_event_sequences;
