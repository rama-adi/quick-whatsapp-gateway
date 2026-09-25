package main

import (
	"database/sql"
	"net/http"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

func runE2ELostGatewayResponse(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("lost private response and gateway restart replay one WhatsApp send", func(t *testing.T) {
		before := len(gateway.getCaptures(t))
		gateway.fault(t, "drop_response")
		status, result := infra.send(t, e2eOrgAKey, "lost-engine-response-1", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "survive lost ack",
		})
		if status == http.StatusOK {
			t.Fatalf("lost private response returned success: %+v", result)
		}
		captures := gateway.waitCaptureCount(t, before+1)
		waID := captures[len(captures)-1].ID
		gateway.stop()
		gateway.run(t, infra)
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "replayed gateway result and API sent projection", func() bool {
			var state string
			var stored sql.NullString
			err := infra.db.QueryRowContext(ctx,
				`SELECT status, wa_message_id FROM outbox
				 WHERE organization_id=? AND idempotency_key=?`,
				e2eOrgA, "lost-engine-response-1",
			).Scan(&state, &stored)
			if err != nil {
				return false
			}
			if state == "failed" {
				t.Fatal("ambiguous send became terminal failure despite gateway ledger result")
			}
			return state == "sent" && stored.String == waID
		})
		if captures := gateway.getCaptures(t); len(captures) != before+1 {
			t.Fatalf("gateway replay sent again: before=%d after=%d", before, len(captures))
		}
		var historyCount, eventCount int
		if err := infra.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
			e2eSessionID, waID,
		).Scan(&historyCount); err != nil {
			t.Fatal(err)
		}
		if err := infra.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM event_log WHERE organization_id=? AND session_id=?
			 AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.waMessageId'))=?`,
			e2eOrgA, e2eSessionID, waID,
		).Scan(&eventCount); err != nil {
			t.Fatal(err)
		}
		if historyCount != 1 || eventCount != 1 {
			t.Fatalf("replayed send persisted %d history rows, %d sent events", historyCount, eventCount)
		}
	})
	t.Run("lost reaction response and gateway restart replay one WhatsApp reaction", func(t *testing.T) {
		const key = "lost-reaction-response-1"
		const messageID = "reaction-source-1"
		path := "/api/v1/sessions/" + e2eSessionID + "/messages/" + messageID + "/reaction"
		body := map[string]string{"chat": e2eGroupJID, "emoji": "👍"}
		before := len(gateway.getCaptures(t))
		gateway.fault(t, "drop_response")
		var first outbound.SendResult
		status := infra.request(t, http.MethodPost, path, e2eOrgAKey, body, &first,
			map[string]string{"Idempotency-Key": key})
		if status == http.StatusOK {
			t.Fatalf("lost private reaction response returned success: %+v", first)
		}
		captures := gateway.waitCaptureCount(t, before+1)
		waID := captures[before].ID
		gateway.stop()
		gateway.run(t, infra)
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "replayed reaction result and API sent projection", func() bool {
			var state string
			var stored sql.NullString
			err := infra.db.QueryRowContext(ctx,
				`SELECT status, wa_message_id FROM outbox
				 WHERE organization_id=? AND idempotency_key=?`, e2eOrgA, key,
			).Scan(&state, &stored)
			if err != nil {
				return false
			}
			if state == "failed" {
				t.Fatal("ambiguous reaction became terminal failure")
			}
			return state == "sent" && stored.String == waID
		})
		var replay outbound.SendResult
		status = infra.request(t, http.MethodPost, path, e2eOrgAKey, body, &replay,
			map[string]string{"Idempotency-Key": key})
		if status != http.StatusOK || replay.WAMessageID != waID || !replay.Replayed {
			t.Fatalf("reaction replay = %d %+v, want original %s", status, replay, waID)
		}
		if captures := gateway.getCaptures(t); len(captures) != before+1 {
			t.Fatalf("reaction replay sent twice: before=%d after=%d", before, len(captures))
		}
	})
}
