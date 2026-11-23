-- An in-flight webhook delivery has an explicit state instead of a lease
-- hidden in next_retry_at: status 'delivering' with a per-claim token and
-- lease_until. Mark* writes are fenced by the token, so a slow worker whose
-- lease expired can neither double-mark nor overwrite a newer outcome.
-- Appending an ENUM member and adding NULL columns are metadata-only changes.
ALTER TABLE webhook_deliveries
  MODIFY COLUMN status ENUM('pending','delivered','failed','dead','delivering') NOT NULL DEFAULT 'pending',
  ADD COLUMN claim_token CHAR(26) NULL,
  ADD COLUMN lease_until BIGINT NULL;
