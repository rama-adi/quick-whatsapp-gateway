-- Explicit post-commit fan-out work for every committed event (gateway ingest
-- and API-originated events). gateway_ingested_events keeps only dedupe and
-- journal provenance; the work state that used to be implied by
-- completed_at IS NULL moves to event_fanout with an explicit status, a
-- per-claim token, a lease, an attempt count, the last error and a dead-letter
-- state that unblocks the session.
--
-- session_seq is assigned by the API inside the committing transaction from
-- session_event_sequences, so it is unique per session and follows commit
-- order (and journal order inside one gateway batch). Fan-out processes a
-- session strictly in session_seq order: only the lowest pending/leased row
-- of a session (its head) is claimable, and only while no live lease holds it.

CREATE TABLE session_event_sequences (
  session_id VARCHAR(64) NOT NULL PRIMARY KEY,
  last_seq   BIGINT UNSIGNED NOT NULL,
  CONSTRAINT fk_session_event_sequences_session FOREIGN KEY (session_id)
    REFERENCES wa_sessions(id) ON DELETE CASCADE
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE event_fanout (
  event_id        VARCHAR(64) NOT NULL PRIMARY KEY,
  organization_id VARCHAR(64) NOT NULL,
  session_id      VARCHAR(64) NOT NULL,
  session_seq     BIGINT UNSIGNED NOT NULL,
  source          ENUM('gateway','api') NOT NULL,
  status          ENUM('pending','leased','done','dead') NOT NULL DEFAULT 'pending',
  attempts        INT UNSIGNED NOT NULL DEFAULT 0,
  next_attempt_at BIGINT NOT NULL,
  claim_token     CHAR(26) NULL,
  claimed_by      VARCHAR(255) NULL,
  lease_until     BIGINT NULL,
  last_error      TEXT NULL,
  created_at      BIGINT NOT NULL,
  finished_at     BIGINT NULL,
  UNIQUE KEY uq_event_fanout_session_seq (session_id, session_seq),
  KEY idx_event_fanout_head (session_id, status, session_seq),
  KEY idx_event_fanout_due (status, next_attempt_at),
  KEY idx_event_fanout_claim (claim_token),
  KEY idx_event_fanout_finished (status, finished_at),
  CONSTRAINT fk_event_fanout_session FOREIGN KEY (session_id)
    REFERENCES wa_sessions(id) ON DELETE CASCADE,
  CONSTRAINT chk_event_fanout_lease CHECK (
    (status = 'leased') = (claim_token IS NOT NULL AND lease_until IS NOT NULL)),
  CONSTRAINT chk_event_fanout_finished CHECK (
    (status IN ('done','dead')) = (finished_at IS NOT NULL))
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Carry over only unfinished work (uses idx_gateway_ingested_work; cheap).
-- Existing order (committed_at, event_log_id) becomes the initial session_seq.
INSERT INTO event_fanout
  (event_id, organization_id, session_id, session_seq, source, status, attempts,
   next_attempt_at, created_at)
SELECT i.event_log_id, i.organization_id, i.session_id,
       ROW_NUMBER() OVER (PARTITION BY i.session_id ORDER BY i.committed_at, i.event_log_id),
       'gateway', 'pending', 0, i.committed_at, i.committed_at
FROM gateway_ingested_events i
WHERE i.completed_at IS NULL;

INSERT INTO session_event_sequences (session_id, last_seq)
SELECT session_id, MAX(session_seq) FROM event_fanout GROUP BY session_id;

-- Separate statements keep each ALTER on its cheapest algorithm: dropping an
-- index is in-place metadata work, and on MySQL 8.0.29+ (8.4 in every compose
-- file) dropping and adding columns is INSTANT, so the large dedupe table is
-- not rebuilt.
ALTER TABLE gateway_ingested_events DROP KEY idx_gateway_ingested_work;
ALTER TABLE gateway_ingested_events
  DROP COLUMN completed_at,
  DROP COLUMN lease_until,
  DROP COLUMN claimed_by,
  ADD COLUMN journal_sequence BIGINT UNSIGNED NOT NULL DEFAULT 0;
