ALTER TABLE outbox
  DROP COLUMN effect_possible,
  DROP COLUMN acknowledged_at,
  DROP COLUMN expires_at;
