-- Durable record of user-initiated WhatsApp backup (crypt15) imports. This table
-- is both the job-status surface (the dashboard polls it) and the source of truth
-- for the once-per-24h-per-session quota (super_admins bypass). See
-- docs/specs/backfill-import.md.
CREATE TABLE backfill_imports (
  id                 VARCHAR(64) PRIMARY KEY,            -- bf_<ULID>
  session_id         VARCHAR(64) NOT NULL,
  organization_id    VARCHAR(64) NOT NULL,
  source             VARCHAR(32) NOT NULL DEFAULT 'crypt15',
  status             ENUM('running','succeeded','failed') NOT NULL DEFAULT 'running',
  chats              INT NOT NULL DEFAULT 0,
  messages           INT NOT NULL DEFAULT 0,
  identities         INT NOT NULL DEFAULT 0,
  groups_count       INT NOT NULL DEFAULT 0,
  group_members      INT NOT NULL DEFAULT 0,
  schema_fingerprint VARCHAR(255) NULL,                  -- detected build-id + capability hash
  error              TEXT NULL,
  created_at         BIGINT NOT NULL,
  finished_at        BIGINT NULL,
  KEY idx_backfill_session (session_id, created_at)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
