-- Keep daily retention pruning bounded and index-driven as the history tables grow.
CREATE INDEX idx_deliv_retention
  ON webhook_deliveries (status, created_at, id);

CREATE INDEX idx_deliv_event_status
  ON webhook_deliveries (event_id, status);

CREATE INDEX idx_msg_retention
  ON messages (timestamp, id);

CREATE INDEX idx_event_retention
  ON event_log (created_at, id);
