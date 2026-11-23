ALTER TABLE media_assets ADD COLUMN notification JSON NULL;
UPDATE media_assets SET next_attempt_at = 9223372036854775807 WHERE next_attempt_at IS NULL;
ALTER TABLE media_assets
  DROP COLUMN lease_until,
  DROP COLUMN claim_token,
  MODIFY COLUMN next_attempt_at BIGINT NOT NULL;
