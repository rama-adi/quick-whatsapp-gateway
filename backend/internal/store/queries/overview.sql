-- Workspace activity summary (GET /api/v1/overview). Every bucketed query
-- receives the window's bucket starts after the first as `bounds`; MySQL's
-- INTERVAL(x, b1, ..., bn) binary-searches them and returns the zero-based
-- bucket index, so the caller's time zone alignment (computed in Go) is applied
-- exactly, including half-hour offsets and DST days. The [from_ms, to_ms)
-- predicate keeps each scan inside the window. FORCE INDEX pins the covering
-- 0020 activity indexes: with skewed statistics MySQL otherwise picks a shorter
-- prefix index and reads base rows (or, for EXISTS, other tenants' rows).

-- name: OverviewOrgSessionIDs :many
SELECT id FROM wa_sessions WHERE organization_id = ?;

-- name: OverviewMessageBuckets :many
SELECT CAST(INTERVAL(timestamp, sqlc.slice('bounds')) AS SIGNED) AS bucket, direction, COUNT(*) AS n
FROM messages FORCE INDEX (idx_msg_activity)
WHERE session_id IN (sqlc.slice('session_ids'))
  AND direction IN ('in', 'out')
  AND timestamp >= sqlc.arg(from_ms) AND timestamp < sqlc.arg(to_ms)
GROUP BY bucket, direction;

-- name: OverviewActiveChats :one
SELECT COUNT(*) AS n
FROM (
  SELECT DISTINCT session_id, chat_jid
  FROM messages FORCE INDEX (idx_msg_activity)
  WHERE session_id IN (sqlc.slice('session_ids'))
    AND direction IN ('in', 'out')
    AND timestamp >= sqlc.arg(from_ms) AND timestamp < sqlc.arg(to_ms)
) AS active_chats;

-- name: OverviewFailedSendBuckets :many
SELECT CAST(INTERVAL(created_at, sqlc.slice('bounds')) AS SIGNED) AS bucket, COUNT(*) AS n
FROM outbox FORCE INDEX (idx_outbox_activity)
WHERE organization_id = sqlc.arg(organization_id)
  AND status = 'failed'
  AND created_at >= sqlc.arg(from_ms) AND created_at < sqlc.arg(to_ms)
  AND session_id IN (sqlc.slice('session_ids'))
GROUP BY bucket;

-- name: OverviewOrgWebhookIDs :many
SELECT id FROM webhooks WHERE organization_id = ?;

-- name: OverviewWebhookDeliveryBuckets :many
-- Webhook ids are resolved first (OverviewOrgWebhookIDs) so this is a
-- single-table, index-only range scan. The hint pins the covering
-- idx_deliv_activity: MySQL otherwise tends to choose a ref lookup on the
-- uq_deliv_webhook_event prefix and read every delivery row of the webhook.
SELECT CAST(INTERVAL(created_at, sqlc.slice('bounds')) AS SIGNED) AS bucket, status, COUNT(*) AS n
FROM webhook_deliveries FORCE INDEX (idx_deliv_activity)
WHERE webhook_id IN (sqlc.slice('webhook_ids'))
  AND status IN ('delivered', 'failed', 'dead')
  AND created_at >= sqlc.arg(from_ms) AND created_at < sqlc.arg(to_ms)
GROUP BY bucket, status;

-- name: OverviewSessionWebhookDeliveryBuckets :many
-- Deliveries carry no session column; the session comes from the delivered
-- event's event_log row. STRAIGHT_JOIN keeps the in-window deliveries as the
-- driving range so each costs one uq_event_id lookup, never an event_log scan.
SELECT CAST(INTERVAL(d.created_at, sqlc.slice('bounds')) AS SIGNED) AS bucket, d.status, COUNT(*) AS n
FROM webhook_deliveries d FORCE INDEX (idx_deliv_activity)
STRAIGHT_JOIN event_log e ON e.event_id = d.event_id
WHERE d.webhook_id IN (sqlc.slice('webhook_ids'))
  AND e.session_id = sqlc.arg(session_id)
  AND d.status IN ('delivered', 'failed', 'dead')
  AND d.created_at >= sqlc.arg(from_ms) AND d.created_at < sqlc.arg(to_ms)
GROUP BY bucket, d.status;

-- name: OverviewHasSentOutbox :one
-- The hint keeps the lookup on (organization_id, status): without it MySQL may
-- pick idx_outbox_due (status only) and walk every organization's sent rows
-- when this one has none.
SELECT EXISTS(
  SELECT 1 FROM outbox FORCE INDEX (idx_outbox_activity)
  WHERE organization_id = ? AND status = 'sent'
) AS has_sent;

-- name: OverviewHasOutgoingMessage :one
SELECT EXISTS(
  SELECT 1 FROM messages FORCE INDEX (idx_msg_activity) WHERE session_id IN (sqlc.slice('session_ids')) AND direction = 'out'
) AS has_sent;
