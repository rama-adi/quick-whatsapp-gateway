package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func runE2EEchoBeforeProjection(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("WhatsApp echo before API projection still emits one sent event", func(t *testing.T) {
		const key = "echo-before-projection"
		if _, err := infra.db.Exec(`CREATE TRIGGER e2e_block_early_echo_projection
			BEFORE INSERT ON messages FOR EACH ROW
			SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='e2e delayed API projection'`); err != nil {
			t.Fatal(err)
		}
		before := len(gateway.getCaptures(t))
		status, _ := infra.send(t, e2eOrgAKey, key, domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "early own echo",
		})
		if status == http.StatusOK {
			t.Fatal("projection failure was acknowledged")
		}
		capture := gateway.waitCaptureCount(t, before+1)[before]
		body, err := json.Marshal(map[string]any{
			"id": capture.ID, "chat": e2eGroupJID,
			"sender": e2eDeviceLID, "fromMe": true,
			"message": map[string]any{"conversation": "early own echo"},
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
			t.Fatalf("inject early own echo: %d", response.StatusCode)
		}
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "early own echo committed before projection", func() bool {
			var events int
			if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_log WHERE organization_id=?
				AND session_id=? AND type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.waMessageId'))=?`,
				e2eOrgA, e2eSessionID, domain.EventMessageFromMe, capture.ID).Scan(&events); err != nil {
				return false
			}
			return events == 1
		})
		if _, err := infra.db.Exec(`DROP TRIGGER e2e_block_early_echo_projection`); err != nil {
			t.Fatal(err)
		}
		e2eEventually(t, ctx, "early echo and API outbox recovery", func() bool {
			var state string
			var rows int
			if err := infra.db.QueryRowContext(ctx, `SELECT status FROM outbox WHERE organization_id=? AND idempotency_key=?`,
				e2eOrgA, key).Scan(&state); err != nil {
				return false
			}
			if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
				e2eSessionID, capture.ID).Scan(&rows); err != nil {
				return false
			}
			return state == "sent" && rows == 1
		})
		var ingestedBefore int
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_ingested_events
			WHERE organization_id=? AND session_id=? AND completed_at IS NOT NULL`,
			e2eOrgA, e2eSessionID).Scan(&ingestedBefore); err != nil {
			t.Fatal(err)
		}
		response, err = http.Post(gateway.controlURL+"/incoming", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("inject replayed early echo: %d", response.StatusCode)
		}
		e2eEventually(t, ctx, "replayed early echo acknowledged", func() bool {
			var completed int
			if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_ingested_events
				WHERE organization_id=? AND session_id=? AND completed_at IS NOT NULL`,
				e2eOrgA, e2eSessionID).Scan(&completed); err != nil {
				return false
			}
			return completed > ingestedBefore
		})
		var events int
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_log WHERE organization_id=?
			AND session_id=? AND type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.waMessageId'))=?`,
			e2eOrgA, e2eSessionID, domain.EventMessageFromMe, capture.ID).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if events != 1 {
			t.Fatalf("early echo produced %d sent events", events)
		}
		if captures := gateway.getCaptures(t); len(captures) != before+1 {
			t.Fatalf("early echo recovery redispatched WhatsApp: before=%d after=%d", before, len(captures))
		}
	})
}
