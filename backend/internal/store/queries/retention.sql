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
      AND webhook_deliveries.status IN ('pending', 'failed', 'delivering')
  )
  AND NOT EXISTS (
    SELECT 1
    FROM event_fanout
    WHERE event_fanout.event_id = event_log.event_id
      AND event_fanout.status IN ('pending', 'leased')
  )
ORDER BY created_at, id
LIMIT ?;

-- name: DeleteFinishedEventFanoutBefore :execrows
DELETE FROM event_fanout
WHERE status = ? AND finished_at < ?
ORDER BY finished_at
LIMIT ?;

-- name: DeleteGatewayIngestedEventsBefore :execrows
DELETE FROM gateway_ingested_events
WHERE committed_at < ?
ORDER BY committed_at
LIMIT ?;

-- name: DeleteHistorySyncBatchesBefore :execrows
DELETE FROM history_sync_batches
WHERE applied_at < ?
ORDER BY applied_at
LIMIT ?;

-- name: DeleteOutgoingMessageEventClaimsBefore :execrows
DELETE FROM outgoing_message_event_claims
WHERE created_at < ?
ORDER BY created_at
LIMIT ?;

-- name: DeleteGatewayEventRejectionsBefore :execrows
DELETE FROM gateway_event_rejections
WHERE rejected_at < ?
ORDER BY rejected_at
LIMIT ?;
