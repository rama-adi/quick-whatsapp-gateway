-- Observed event-journal pressure from the last control-mode heartbeat.
-- NULL means the gateway has not reported telemetry (no journal configured or
-- an unreadable journal this cycle); a heartbeat without telemetry preserves
-- the previously reported values via COALESCE in the heartbeat write.
ALTER TABLE gateways
  ADD COLUMN journal_state   ENUM('healthy','degraded','paused','critical') NULL,
  ADD COLUMN journal_entries BIGINT UNSIGNED NULL,
  ADD COLUMN journal_bytes   BIGINT UNSIGNED NULL;
