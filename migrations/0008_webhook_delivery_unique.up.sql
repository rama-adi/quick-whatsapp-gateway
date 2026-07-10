ALTER TABLE webhook_deliveries
  ADD UNIQUE KEY uq_deliv_webhook_event (webhook_id, event_id);
