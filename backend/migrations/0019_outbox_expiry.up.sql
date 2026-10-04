ALTER TABLE outbox
  ADD COLUMN expires_at BIGINT NULL AFTER next_attempt_at,
  ADD COLUMN acknowledged_at BIGINT NULL AFTER wa_message_id,
  ADD COLUMN effect_possible BOOLEAN NOT NULL DEFAULT FALSE AFTER acknowledged_at;

-- Before this release, a queued retry (or an interrupted sending row) may
-- already have reached WhatsApp. No stored evidence can prove otherwise.
-- Preserve that uncertainty before the new scheduler starts claiming rows.
UPDATE outbox
SET effect_possible = TRUE
WHERE terminal_at IS NULL AND (attempts > 0 OR status = 'sending');
