-- First writer owns the public event for one WhatsApp outgoing message.
-- The API send and a gateway echo can race; this key keeps one event identity
-- while gateway journal event IDs remain independently acknowledgeable.
CREATE TABLE outgoing_message_event_claims (
  organization_id VARCHAR(64) NOT NULL,
  session_id VARCHAR(64) NOT NULL,
  chat_jid VARCHAR(255) NOT NULL,
  wa_message_id VARCHAR(255) NOT NULL,
  event_id VARCHAR(64) NOT NULL,
  owner ENUM('api','gateway') NOT NULL,
  PRIMARY KEY (organization_id, session_id, chat_jid, wa_message_id),
  UNIQUE KEY uq_outgoing_message_event_claim (event_id),
  KEY idx_outgoing_message_event_session (session_id),
  CONSTRAINT fk_outgoing_message_event_session FOREIGN KEY (session_id)
    REFERENCES wa_sessions(id) ON DELETE CASCADE
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
