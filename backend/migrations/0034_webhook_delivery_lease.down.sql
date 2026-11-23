UPDATE webhook_deliveries SET status = 'pending', next_retry_at = COALESCE(lease_until, next_retry_at)
WHERE status = 'delivering';
ALTER TABLE webhook_deliveries
  DROP COLUMN lease_until, DROP COLUMN claim_token,
  MODIFY COLUMN status ENUM('pending','delivered','failed','dead') NOT NULL DEFAULT 'pending';
