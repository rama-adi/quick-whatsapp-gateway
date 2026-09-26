-- Existing unknown rows cannot be represented by the previous enum. Stop the
-- rollback until an operator has reconciled them to definite outcomes.
ALTER TABLE outbox
  MODIFY COLUMN status ENUM('queued','sending','sent','failed') NOT NULL DEFAULT 'queued';
