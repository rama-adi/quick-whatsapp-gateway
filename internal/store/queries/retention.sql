-- name: DeleteTerminalWebhookDeliveriesBefore :execrows
DELETE FROM webhook_deliveries
WHERE created_at < ?
  AND status IN (?, ?)
ORDER BY created_at, id
LIMIT ?;

-- name: DeleteMessagesBefore :execrows
DELETE FROM messages
WHERE timestamp < ?
ORDER BY timestamp, id
LIMIT ?;

-- name: DeleteUnreferencedEventLogBefore :execrows
DELETE FROM event_log
WHERE event_log.created_at < ?
  AND NOT EXISTS (
    SELECT 1
    FROM webhook_deliveries
    WHERE webhook_deliveries.event_id = event_log.event_id
      AND webhook_deliveries.status IN (?, ?)
  )
ORDER BY created_at, id
LIMIT ?;
