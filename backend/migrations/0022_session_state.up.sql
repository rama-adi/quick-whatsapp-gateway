-- Split what a user asked a session to do from what its gateway observed.
--
-- wa_sessions.status mixed both: user actions wrote starting/stopped, the
-- status projection overwrote it with whatever the gateway reported, and the
-- desired-state snapshot derived RUN from it. A late or failed status could
-- therefore undo a Start or stop a session nobody stopped.
--
-- * Intent stays on wa_sessions and is written only by user actions:
--   desired_state (run|stop) and start_generation, which every explicit
--   Start/Restart bumps so a gateway restarts a failed session only when the
--   user asked again.
-- * Observation moves to wa_session_runtime and is written only from the
--   gateway (session.status events and desired-state reports), applied only
--   when its status_seq is newer than the stored one.

ALTER TABLE wa_sessions
  ADD COLUMN desired_state ENUM('run','stop') NOT NULL DEFAULT 'stop' AFTER status,
  ADD COLUMN start_generation BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER desired_state;

-- A session that was running, starting or pairing keeps running. An unpaired
-- session among them resumes QR pairing on its gateway.
UPDATE wa_sessions
SET desired_state = 'run'
WHERE status IN ('starting', 'scan_qr_code', 'working');

CREATE TABLE wa_session_runtime (
  session_id        VARCHAR(64) PRIMARY KEY,
  status            ENUM('starting','scan_qr_code','working','reconnecting','failed','stopped','logged_out')
                    NOT NULL DEFAULT 'stopped',
  -- Why the session failed; set only while status is failed (a failed row
  -- migrated from wa_sessions.status has no recorded reason).
  failure_reason    ENUM('passkey_failed','qr_timeout','pairing_error','client_outdated','stream_replaced',
                         'temporary_ban','connect_rejected','connect_error') NULL,
  -- The gateway's ordering key for this status (a hybrid logical clock:
  -- monotonic per gateway process and above the previous process's values).
  -- 0 = no gateway observation yet; any observation replaces it.
  status_seq        BIGINT UNSIGNED NOT NULL DEFAULT 0,
  -- When the gateway observed the status change, epoch-ms.
  status_changed_at BIGINT NOT NULL,
  CONSTRAINT fk_wa_session_runtime_session
    FOREIGN KEY (session_id) REFERENCES wa_sessions(id) ON DELETE CASCADE,
  CONSTRAINT chk_wa_session_runtime_failure CHECK (failure_reason IS NULL OR status = 'failed')
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

INSERT INTO wa_session_runtime (session_id, status, failure_reason, status_seq, status_changed_at)
SELECT id, status, NULL, 0, updated_at
FROM wa_sessions;

ALTER TABLE wa_sessions DROP COLUMN status;
