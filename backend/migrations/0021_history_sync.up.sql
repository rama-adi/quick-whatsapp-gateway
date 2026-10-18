-- History sync on (re)link. The phone sends the chat history it still has
-- after a device link; the gateway converts it into history.batch events and
-- the API projects them here.

-- Where a message row came from. live = captured from a live event or our own
-- send; history = WhatsApp history sync; backup = crypt15 backup import.
-- Appended last so MySQL can add it with ALGORITHM=INSTANT on a large table.
ALTER TABLE messages
  ADD COLUMN origin ENUM('live','history','backup') NOT NULL DEFAULT 'live';

-- history_sync_days: how many days of history to import on (re)link; 0 = off.
--   The API applies its HISTORY_SYNC_DEFAULT_DAYS when a create omits it and
--   caps every value at HISTORY_SYNC_MAX_DAYS.
-- data_account_jid: phone JID (without device suffix) of the WhatsApp account
--   whose chats this session's rows belong to. Set at pairing and NOT cleared
--   on logout, so a relink to a different number is detectable.
-- account_changed_at: when a pairing attached a different account than
--   data_account_jid (stored rows now mix two accounts; nothing is purged).
ALTER TABLE wa_sessions
  ADD COLUMN history_sync_days SMALLINT UNSIGNED NOT NULL DEFAULT 30,
  ADD COLUMN data_account_jid VARCHAR(255) NULL,
  ADD COLUMN account_changed_at BIGINT NULL;

UPDATE wa_sessions SET data_account_jid = CONCAT(phone_number, '@s.whatsapp.net')
WHERE phone_number IS NOT NULL AND phone_number <> '';

-- One row per gateway history import run (history.sync runId). Gateway
-- counters (chunks..skipped) mirror the latest history.sync event and only
-- grow; the messages_* and batches_applied counters are the API's own result,
-- incremented once per history.batch under the history_sync_batches ledger.
CREATE TABLE history_sync_runs (
  id                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id        VARCHAR(64) NOT NULL,
  run_id            VARCHAR(64) NOT NULL,
  account_jid       VARCHAR(255) NULL,
  status            ENUM('running','completed','failed') NOT NULL DEFAULT 'running',
  sync_type         VARCHAR(32) NULL,
  progress          TINYINT UNSIGNED NULL,
  cutoff_at         BIGINT NULL,
  chunks            INT NOT NULL DEFAULT 0,
  batches           INT NOT NULL DEFAULT 0,
  conversations     INT NOT NULL DEFAULT 0,
  messages          INT NOT NULL DEFAULT 0,
  skipped           INT NOT NULL DEFAULT 0,
  batches_applied   INT NOT NULL DEFAULT 0,
  messages_imported INT NOT NULL DEFAULT 0,
  messages_existing INT NOT NULL DEFAULT 0,
  messages_dropped  INT NOT NULL DEFAULT 0,
  error             TEXT NULL,
  started_at        BIGINT NOT NULL,
  updated_at        BIGINT NOT NULL,
  finished_at       BIGINT NULL,
  UNIQUE KEY uq_hsr_run (session_id, run_id),
  KEY idx_hsr_latest (session_id, started_at),
  CONSTRAINT fk_hsr_session FOREIGN KEY (session_id) REFERENCES wa_sessions(id) ON DELETE CASCADE
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Idempotency ledger for history.batch projection, keyed by event id. The
-- committed-event worker can redeliver an event; a present row means the batch
-- is already applied (rows and run counters), so the replay is a no-op.
-- (dbconn sets clientFoundRows=true, so ON DUPLICATE KEY rowsAffected cannot
-- tell an insert from a duplicate; the ledger is checked explicitly.)
CREATE TABLE history_sync_batches (
  event_id          VARCHAR(64) NOT NULL PRIMARY KEY,
  session_id        VARCHAR(64) NOT NULL,
  run_id            VARCHAR(64) NOT NULL,
  messages_imported INT NOT NULL DEFAULT 0,
  messages_existing INT NOT NULL DEFAULT 0,
  messages_dropped  INT NOT NULL DEFAULT 0,
  applied_at        BIGINT NOT NULL,
  KEY idx_hsb_run (session_id, run_id),
  CONSTRAINT fk_hsb_session FOREIGN KEY (session_id) REFERENCES wa_sessions(id) ON DELETE CASCADE
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
