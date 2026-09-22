-- The outbox is the API's durable send-command table. Each row id doubles as
-- the stable command_id the gateway ledger deduplicates on, so a retried or
-- reconciled send re-issues the same identity and can never duplicate a
-- WhatsApp delivery within the idempotency window.
--
-- next_attempt_at schedules retries with backoff; ambiguous outcomes (gateway
-- unreachable, deadline lost) return to 'queued' with a future attempt time
-- instead of reporting a definite failure. terminal_at records when a row
-- reached sent/failed for observability.
ALTER TABLE outbox
  ADD COLUMN next_attempt_at BIGINT NOT NULL DEFAULT 0 AFTER attempts,
  ADD COLUMN terminal_at BIGINT NULL AFTER error,
  ADD KEY idx_outbox_due (status, next_attempt_at);

UPDATE outbox SET terminal_at = updated_at WHERE status IN ('sent', 'failed') AND terminal_at IS NULL;
