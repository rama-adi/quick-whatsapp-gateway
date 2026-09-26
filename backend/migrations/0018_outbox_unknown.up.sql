-- An exhausted ambiguous command remains an unknown remote outcome. It must
-- never be replayed as a definite failure or claimed for another dispatch.
ALTER TABLE outbox
  MODIFY COLUMN status ENUM('queued','sending','sent','failed','unknown') NOT NULL DEFAULT 'queued';
