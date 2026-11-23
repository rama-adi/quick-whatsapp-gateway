-- Media asset transfers get an explicit claim lease (claim_token,
-- lease_until) instead of a row lock held across S3 and WhatsApp I/O, an
-- attempt cap that ends in status 'failed', and a nullable next_attempt_at
-- (NULL = no further work) instead of the sentinel 1<<63-1.
--
-- media.ready / media.expired events now enqueue durable fan-out
-- (event_fanout, source api) in the same transaction as the status change,
-- replacing the worker's private notification column. Notifications that
-- were not published yet are carried over first.
--
-- MODIFY ... NULL rebuilds media_assets (online, in place).
ALTER TABLE media_assets
  MODIFY COLUMN next_attempt_at BIGINT NULL,
  ADD COLUMN claim_token CHAR(26) NULL,
  ADD COLUMN lease_until BIGINT NULL;

INSERT INTO event_fanout
  (event_id, organization_id, session_id, session_seq, source, status, attempts, next_attempt_at, created_at)
SELECT x.event_id, x.organization_id, x.session_id, x.rn + COALESCE(q.last_seq, 0), 'api', 'pending', 0,
       x.created_at, x.created_at
FROM (
  SELECT e.event_id, e.organization_id, e.session_id, e.created_at,
         ROW_NUMBER() OVER (PARTITION BY e.session_id ORDER BY e.created_at, e.event_id) AS rn
  FROM media_assets m
  JOIN event_log e ON e.event_id = JSON_UNQUOTE(JSON_EXTRACT(m.notification, '$.id'))
  JOIN wa_sessions s ON s.id = e.session_id
  WHERE m.notification IS NOT NULL
    AND NOT EXISTS (SELECT 1 FROM event_fanout f WHERE f.event_id = e.event_id)
) x
LEFT JOIN session_event_sequences q ON q.session_id = x.session_id;

INSERT INTO session_event_sequences (session_id, last_seq)
SELECT t.session_id, t.max_seq FROM (
  SELECT session_id, MAX(session_seq) AS max_seq FROM event_fanout GROUP BY session_id
) t
ON DUPLICATE KEY UPDATE last_seq = GREATEST(last_seq, t.max_seq);

UPDATE media_assets
SET next_attempt_at = IF(status = 'ready', expires_at, NULL)
WHERE notification IS NOT NULL;

UPDATE media_assets SET next_attempt_at = NULL WHERE next_attempt_at = 9223372036854775807;

ALTER TABLE media_assets DROP COLUMN notification;
