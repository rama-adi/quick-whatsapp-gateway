package main

import (
	"net/http"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

func runE2EAsyncSend(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("async HTTP acceptance drains to one sent history and replay", func(t *testing.T) {
		const key = "async-drain-1"
		before := len(gateway.getCaptures(t))
		request := domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID, Text: "queued send"}
		var accepted outbound.SendResult
		status := infra.request(t, http.MethodPost,
			"/api/v1/sessions/"+e2eSessionID+"/messages?async=true", e2eOrgAKey,
			request, &accepted, map[string]string{"Idempotency-Key": key})
		if status != http.StatusAccepted || accepted.Mode != outbound.ModeAsync || accepted.OutboxID == "" {
			t.Fatalf("async acceptance = %d %+v", status, accepted)
		}
		ctx, cancel := e2eContext(t)
		defer cancel()
		var waID string
		e2eEventually(t, ctx, "async worker sent projection", func() bool {
			var state string
			var messages int
			if err := infra.db.QueryRowContext(ctx, `SELECT status, wa_message_id FROM outbox WHERE id=?`,
				accepted.OutboxID).Scan(&state, &waID); err != nil || state != "sent" || waID == "" {
				return false
			}
			if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
				e2eSessionID, waID).Scan(&messages); err != nil {
				return false
			}
			return messages == 1
		})
		if captures := gateway.waitCaptureCount(t, before+1); captures[before].ID != waID {
			t.Fatalf("async WA capture mismatch: %+v", captures[before])
		}
		if len(accepted.ReservedMessageIDs) != 1 || accepted.ReservedMessageIDs[0] != waID {
			t.Fatalf("pending reservation did not identify the eventual recipient message: %+v, actual %s", accepted, waID)
		}
		status, replay := infra.send(t, e2eOrgAKey, key, request)
		if status != http.StatusOK || !replay.Replayed || replay.WAMessageID != waID ||
			replay.OutboxID != accepted.OutboxID {
			t.Fatalf("async replay = %d %+v, initial %+v", status, replay, accepted)
		}
		if captures := gateway.getCaptures(t); len(captures) != before+1 {
			t.Fatalf("async replay redispatched WhatsApp: before=%d after=%d", before, len(captures))
		}
	})
}
