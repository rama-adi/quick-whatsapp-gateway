-- Bounded retention for durable work and dedupe tables that were never
-- pruned: gateway_ingested_events (journal dedupe), history_sync_batches
-- (history projection ledger), outgoing_message_event_claims (sent-event
-- owner arbitration), plus event_fanout and gateway_event_rejections. The API
-- maintenance loop deletes rows older than fixed windows in bounded batches;
-- these indexes make each batch an index range scan.
--
-- outgoing_message_event_claims had no timestamp: existing rows get the
-- migration time, so they age out one window after the deploy. The UPDATE
-- touches every claim row once; the index builds run online (in place).
ALTER TABLE outgoing_message_event_claims ADD COLUMN created_at BIGINT NOT NULL DEFAULT 0;
UPDATE outgoing_message_event_claims
SET created_at = CAST(UNIX_TIMESTAMP(NOW(3)) * 1000 AS SIGNED)
WHERE created_at = 0;
ALTER TABLE outgoing_message_event_claims ADD KEY idx_outgoing_message_event_created (created_at);
ALTER TABLE gateway_ingested_events ADD KEY idx_gateway_ingested_committed (committed_at);
ALTER TABLE history_sync_batches ADD KEY idx_hsb_applied (applied_at);
