-- 0005_polls: store poll-creation options so incoming poll votes can be resolved.
--
-- A poll vote (PollUpdateMessage) carries only SHA-256 hashes of the selected
-- option names, never the names themselves. To turn a vote back into readable
-- option text we need the originating poll's option list. We keep it in its own
-- table (the canonical source of truth for a poll's options) rather than copying
-- options onto every vote row.
CREATE TABLE polls (
  id               BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id       VARCHAR(64) NOT NULL,
  poll_message_id  VARCHAR(255) NOT NULL,           -- wa_message_id of the poll-creation message
  chat_jid         VARCHAR(255) NOT NULL,
  name             TEXT NULL,                        -- the poll question
  options          JSON NOT NULL,                    -- ["Yes","No"] in creation order
  selectable_count INT NOT NULL DEFAULT 1,
  created_at       BIGINT NOT NULL,
  updated_at       BIGINT NOT NULL,
  UNIQUE KEY uq_poll (session_id, poll_message_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
