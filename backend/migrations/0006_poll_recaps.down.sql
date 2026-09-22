ALTER TABLE polls
  DROP KEY idx_poll_recap_due,
  DROP COLUMN recap_emitted_at,
  DROP COLUMN hide_votes,
  DROP COLUMN end_time;
