-- 0006_poll_recaps: timed poll metadata + durable recap emission guard.
ALTER TABLE polls
  ADD COLUMN end_time BIGINT NULL AFTER selectable_count,
  ADD COLUMN hide_votes TINYINT(1) NOT NULL DEFAULT 0 AFTER end_time,
  ADD COLUMN recap_emitted_at BIGINT NULL AFTER hide_votes,
  ADD KEY idx_poll_recap_due (end_time, recap_emitted_at);
