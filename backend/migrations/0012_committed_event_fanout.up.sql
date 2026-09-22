-- Post-commit fan-out work state for ingested gateway events. Ingest already
-- commits the durable envelope transactionally; these columns track the
-- remaining consumer work so a crashed worker or failed consumer retries the
-- event through claim leases instead of re-ingesting or duplicating it.
ALTER TABLE gateway_ingested_events
  ADD COLUMN claimed_by   VARCHAR(64) NULL,
  ADD COLUMN lease_until  BIGINT NULL,
  ADD COLUMN completed_at BIGINT NULL,
  ADD KEY idx_gateway_ingested_work (completed_at, lease_until);
