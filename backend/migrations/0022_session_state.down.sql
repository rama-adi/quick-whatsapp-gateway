ALTER TABLE wa_sessions
  ADD COLUMN status ENUM('starting','scan_qr_code','working','failed','stopped','logged_out')
  NOT NULL DEFAULT 'stopped' AFTER label;

UPDATE wa_sessions AS s
JOIN wa_session_runtime AS r ON r.session_id = s.id
SET s.status = CASE r.status WHEN 'reconnecting' THEN 'starting' ELSE r.status END;

DROP TABLE wa_session_runtime;

ALTER TABLE wa_sessions
  DROP COLUMN start_generation,
  DROP COLUMN desired_state;
