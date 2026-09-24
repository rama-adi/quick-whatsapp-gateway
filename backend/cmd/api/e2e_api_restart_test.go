package main

import (
	"net/http"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func runE2EAPIRestartRecovery(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("API restart repairs acknowledged pending outbox without another WhatsApp send", func(t *testing.T) {
		const key = "api-restart-pending-projection"
		if _, err := infra.db.Exec(`CREATE TRIGGER e2e_reject_api_restart_projection
			BEFORE INSERT ON messages FOR EACH ROW
			SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='e2e API restart projection fault'`); err != nil {
			t.Fatal(err)
		}
		before := len(gateway.getCaptures(t))
		status, _ := infra.send(t, e2eOrgAKey, key, domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "API restart recovery",
		})
		if status == http.StatusOK {
			t.Fatal("API acknowledged while projection trigger rejected sent message")
		}
		capture := gateway.waitCaptureCount(t, before+1)[before]
		var state string
		if err := infra.db.QueryRow(`SELECT status FROM outbox WHERE organization_id=? AND idempotency_key=?`,
			e2eOrgA, key).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "sent" || state == "failed" {
			t.Fatalf("projection fault left terminal outbox state %q", state)
		}
		infra.stopAPI()
		if _, err := infra.db.Exec(`DROP TRIGGER e2e_reject_api_restart_projection`); err != nil {
			t.Fatal(err)
		}
		infra.startAPI(t)
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "API restart projection recovery", func() bool {
			var outboxState, waID string
			var history, events int
			if err := infra.db.QueryRowContext(ctx, `SELECT status, wa_message_id FROM outbox
				WHERE organization_id=? AND idempotency_key=?`, e2eOrgA, key).Scan(&outboxState, &waID); err != nil {
				return false
			}
			if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
				e2eSessionID, capture.ID).Scan(&history); err != nil {
				return false
			}
			if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_log WHERE organization_id=?
				AND session_id=? AND type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.waMessageId'))=?`,
				e2eOrgA, e2eSessionID, domain.EventMessageFromMe, capture.ID).Scan(&events); err != nil {
				return false
			}
			if history > 1 || events > 1 {
				t.Fatalf("API restart duplicated history=%d events=%d", history, events)
			}
			return outboxState == "sent" && waID == capture.ID && history == 1 && events == 1
		})
		if captures := gateway.getCaptures(t); len(captures) != before+1 {
			t.Fatalf("API restart redispatched WhatsApp: before=%d after=%d", before, len(captures))
		}
	})
}
