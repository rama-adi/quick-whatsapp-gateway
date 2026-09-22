ALTER TABLE outbox
  DROP COLUMN terminal_at,
  DROP COLUMN next_attempt_at;

-- 0014 also reshaped the status enum comment trail; nothing else to undo.
