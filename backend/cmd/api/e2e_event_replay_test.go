package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func runE2ELostEventAck(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("lost committed event acknowledgement replays one inbound projection", func(t *testing.T) {
		const inboundID = "e2e-inbound-lost-ack"
		gateway.fault(t, "drop_event_ack")
		body, err := json.Marshal(map[string]any{
			"id":        inboundID,
			"chat":      e2eGroupJID,
			"sender":    e2eSenderLID,
			"senderAlt": "6282222222222@s.whatsapp.net",
			"fromMe":    false,
			"message":   map[string]any{"conversation": "event replay after lost ack"},
		})
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(gateway.controlURL+"/incoming", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("inject inbound message: HTTP %d", response.StatusCode)
		}
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "committed inbound event after lost ack", func() bool {
			var messages, events int
			if err := infra.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
				e2eSessionID, inboundID,
			).Scan(&messages); err != nil {
				return false
			}
			if err := infra.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM event_log WHERE organization_id=? AND session_id=?
				 AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.waMessageId'))=?`,
				e2eOrgA, e2eSessionID, inboundID,
			).Scan(&events); err != nil {
				return false
			}
			if messages > 1 || events > 1 {
				t.Fatalf("event replay duplicated messages=%d events=%d", messages, events)
			}
			return messages == 1 && events == 1
		})
		status, messages := infra.messages(t, e2eOrgAKey, e2eGroupJID)
		if status != http.StatusOK {
			t.Fatalf("history HTTP %d", status)
		}
		var matches int
		for _, message := range messages {
			if message.WAMessageID == inboundID && !message.FromMe {
				matches++
			}
		}
		if matches != 1 {
			t.Fatalf("inbound replay history matches=%d", matches)
		}
	})
}
