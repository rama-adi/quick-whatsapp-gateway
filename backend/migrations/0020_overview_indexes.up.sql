-- Window indexes for the workspace overview (GET /api/v1/overview). Each one
-- turns a dashboard window query into a bounded range scan that never reads the
-- base rows.
--
-- messages: per session, seek one direction and range over the WhatsApp
-- timestamp. chat_jid is carried so the distinct-active-chats count is
-- index-only, and (session_id, 'out') answers "has this org ever sent" with one
-- seek.
CREATE INDEX idx_msg_activity
  ON messages (session_id, direction, timestamp, chat_jid);

-- outbox: per organization, seek a status and range over created_at; session_id
-- is carried for the optional session filter. (organization_id, 'sent') also
-- answers the has-sent check with one seek.
CREATE INDEX idx_outbox_activity
  ON outbox (organization_id, status, created_at, session_id);

-- webhook_deliveries: per webhook (reached through webhooks.organization_id),
-- seek a status and range over created_at.
CREATE INDEX idx_deliv_activity
  ON webhook_deliveries (webhook_id, status, created_at);
