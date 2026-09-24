package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func runE2EOwnEcho(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("WhatsApp echo of API send preserves one visible message", func(t *testing.T) {
		status, result := infra.send(t, e2eOrgAKey, "own-echo-source", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "echoed outgoing",
		})
		if status != http.StatusOK || result.WAMessageID == "" {
			t.Fatalf("source send = %d %+v", status, result)
		}
		var ingestedBefore int
		if err := infra.db.QueryRow(`SELECT COUNT(*) FROM gateway_ingested_events WHERE session_id=?
			AND completed_at IS NOT NULL`,
			e2eSessionID).Scan(&ingestedBefore); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]any{
			"id": result.WAMessageID, "chat": e2eGroupJID,
			"sender": e2eDeviceLID, "fromMe": true,
			"message": map[string]any{"conversation": "echoed outgoing"},
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
			t.Fatalf("inject own echo: %d", response.StatusCode)
		}
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "own echo committed", func() bool {
			var ingested int
			if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_ingested_events
				WHERE organization_id=? AND session_id=? AND completed_at IS NOT NULL`,
				e2eOrgA, e2eSessionID).Scan(&ingested); err != nil {
				return false
			}
			return ingested > ingestedBefore
		})
		response, err = http.Post(gateway.controlURL+"/incoming", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("inject replayed own echo: %d", response.StatusCode)
		}
		e2eEventually(t, ctx, "replayed own echo acknowledged", func() bool {
			var ingested int
			if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_ingested_events
				WHERE organization_id=? AND session_id=? AND completed_at IS NOT NULL`,
				e2eOrgA, e2eSessionID).Scan(&ingested); err != nil {
				return false
			}
			return ingested > ingestedBefore+1
		})
		var stored, sentEvents int
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
			e2eSessionID, result.WAMessageID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_log WHERE organization_id=?
			AND session_id=? AND type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.waMessageId'))=?`,
			e2eOrgA, e2eSessionID, domain.EventMessageFromMe, result.WAMessageID).Scan(&sentEvents); err != nil {
			t.Fatal(err)
		}
		if stored != 1 || sentEvents != 1 {
			t.Fatalf("outgoing WhatsApp echo created %d history messages and %d sent events", stored, sentEvents)
		}
	})
	t.Run("linked device own message without API outbox remains visible", func(t *testing.T) {
		const waID = "e2e-linked-device-own-message"
		body, err := json.Marshal(map[string]any{
			"id": waID, "chat": e2eGroupJID,
			"sender": e2eDeviceLID, "fromMe": true,
			"message": map[string]any{"conversation": "from linked device"},
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
			t.Fatalf("inject linked device message: %d", response.StatusCode)
		}
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "linked device own message projection", func() bool {
			var history, events int
			if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
				e2eSessionID, waID).Scan(&history); err != nil {
				return false
			}
			if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_log WHERE organization_id=?
				AND session_id=? AND type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.waMessageId'))=?`,
				e2eOrgA, e2eSessionID, domain.EventMessageFromMe, waID).Scan(&events); err != nil {
				return false
			}
			if history > 1 || events > 1 {
				t.Fatalf("linked device own message duplicated history=%d events=%d", history, events)
			}
			return history == 1 && events == 1
		})
	})
}

func runE2EDelayedReceipts(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("delayed and repeated receipts do not regress outgoing status", func(t *testing.T) {
		status, result := infra.send(t, e2eOrgAKey, "receipt-source", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "receipt ordering",
		})
		if status != http.StatusOK || result.WAMessageID == "" {
			t.Fatalf("receipt source = %d %+v", status, result)
		}
		for _, receipt := range []string{"read", "delivered", "read"} {
			event := domain.NewEvent(domain.EventMessageStatus, e2eSessionID, e2eOrgA, map[string]any{
				"chatJid": e2eGroupJID, "messageIds": []string{result.WAMessageID},
				"status": receipt, "timestamp": domain.NowMs(),
			})
			body, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.Post(gateway.controlURL+"/events", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("inject %s receipt: HTTP %d", receipt, response.StatusCode)
			}
		}
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "read status after out-of-order receipts", func() bool {
			var stored string
			if err := infra.db.QueryRowContext(ctx, `SELECT status FROM messages WHERE session_id=? AND wa_message_id=?`,
				e2eSessionID, result.WAMessageID).Scan(&stored); err != nil {
				return false
			}
			return stored == "read"
		})
		readStatus, messages := infra.messages(t, e2eOrgAKey, e2eGroupJID)
		if readStatus != http.StatusOK {
			t.Fatalf("history HTTP %d", readStatus)
		}
		var matches int
		for _, message := range messages {
			if message.WAMessageID == result.WAMessageID {
				matches++
				if message.Status == nil || *message.Status != domain.MessageRead {
					t.Fatalf("out-of-order receipt regressed status: %+v", message)
				}
			}
		}
		if matches != 1 {
			t.Fatalf("receipt message history rows=%d", matches)
		}
	})
}
