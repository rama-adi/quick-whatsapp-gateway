-- Outbox commands get an explicit claim instead of updated_at doubling as an
-- owner stamp:
--   queued        due for a dispatch attempt (or deferred by the rate limit)
--   sending       leased: claim_token + lease_until name the owner and deadline
--   acknowledged  WhatsApp accepted the send; its projection is retried
--   sent | failed | unknown   terminal (terminal_at set)
-- dispatch_attempts (renamed from attempts) counts only real engine
-- dispatches; rate-limit deferrals and projection retries are counted apart
-- (deferrals, projection_attempts) so a deferral can never use up the
-- ambiguous-send budget, and projection retries are capped.
--
-- The first ALTERs are metadata-only (ENUM member appended, RENAME COLUMN,
-- ADD COLUMN). The CHECK constraints at the end require ALGORITHM=COPY: the
-- outbox table is rebuilt once and blocks writes for the duration.
ALTER TABLE outbox
  MODIFY COLUMN status ENUM('queued','sending','sent','failed','unknown','acknowledged') NOT NULL DEFAULT 'queued';
ALTER TABLE outbox RENAME COLUMN attempts TO dispatch_attempts;
ALTER TABLE outbox
  ADD COLUMN deferrals INT NOT NULL DEFAULT 0,
  ADD COLUMN projection_attempts INT NOT NULL DEFAULT 0,
  ADD COLUMN claim_token CHAR(26) NULL,
  ADD COLUMN lease_until BIGINT NULL;

-- Terminal rows written by the removed UpdateStatus fallback lacked terminal_at.
UPDATE outbox SET terminal_at = updated_at
WHERE status IN ('sent','failed','unknown') AND terminal_at IS NULL;
UPDATE outbox SET terminal_at = NULL
WHERE status IN ('queued','sending') AND terminal_at IS NOT NULL;

-- A queued row that already carries a WhatsApp acknowledgement was a deferred
-- projection.
UPDATE outbox SET status = 'acknowledged'
WHERE status = 'queued' AND acknowledged_at IS NOT NULL;

-- In-flight rows keep the former lease rule (updated_at + 6 min, longer than
-- any engine send deadline) under a legacy token no live worker holds.
UPDATE outbox SET claim_token = 'legacy', lease_until = updated_at + 360000
WHERE status = 'sending';

ALTER TABLE outbox
  ADD KEY idx_outbox_lease (status, lease_until),
  ADD CONSTRAINT chk_outbox_terminal CHECK ((status IN ('sent','failed','unknown')) = (terminal_at IS NOT NULL)),
  ADD CONSTRAINT chk_outbox_lease CHECK ((status = 'sending') = (claim_token IS NOT NULL AND lease_until IS NOT NULL)),
  ADD CONSTRAINT chk_outbox_acknowledged CHECK (status <> 'acknowledged' OR acknowledged_at IS NOT NULL);
