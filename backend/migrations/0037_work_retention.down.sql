ALTER TABLE history_sync_batches DROP KEY idx_hsb_applied;
ALTER TABLE gateway_ingested_events DROP KEY idx_gateway_ingested_committed;
ALTER TABLE outgoing_message_event_claims DROP KEY idx_outgoing_message_event_created, DROP COLUMN created_at;
