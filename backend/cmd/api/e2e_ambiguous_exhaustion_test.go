package main

import (
	"database/sql"
	"net/http"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

// This fault crosses the public API, real MySQL outbox, private engine RPC,
// gateway command journal, and a stateful WhatsApp recipient. The test process
// configures one permitted attempt to reach the exhaustion boundary directly.
func runE2EAmbiguousExhaustion(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("remote commit with every acknowledgement lost stays unknown after attempt exhaustion", func(t *testing.T) {
		const key = "ambiguous-exhaustion-one-effect"
		request := domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID, Text: "one remote effect"}
		infra.stopAPI()
		infra.exhaustAmbiguous = true
		infra.startAPI(t)
		before := len(gateway.getCaptures(t))
		gateway.fault(t, "drop_response")
		status, initial := infra.send(t, e2eOrgAKey, key, request)
		if status == http.StatusOK || status == http.StatusAccepted {
			t.Fatalf("lost acknowledgement was reported as a definite result: %d %+v", status, initial)
		}
		capture := gateway.waitCaptureCount(t, before+1)[before]
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "terminal unknown result after lost acknowledgement", func() bool {
			var state string
			var terminalAt sql.NullInt64
			err := infra.db.QueryRowContext(ctx,
				`SELECT status, terminal_at FROM outbox WHERE organization_id=? AND idempotency_key=?`,
				e2eOrgA, key,
			).Scan(&state, &terminalAt)
			return err == nil && state == string(domain.OutboxUnknown) && terminalAt.Valid
		})
		status, replay := infra.send(t, e2eOrgAKey, key, request)
		if status != http.StatusAccepted || replay.Mode != outbound.ModeAsync ||
			!replay.Replayed || replay.Status == domain.MessageFailed || replay.WAMessageID != "" {
			t.Fatalf("unknown effect replayed as definite: %d %+v", status, replay)
		}
		gateway.fault(t, "none")
		infra.stopAPI()
		infra.exhaustAmbiguous = false
		infra.startAPI(t)
		status, afterRestart := infra.send(t, e2eOrgAKey, key, request)
		if status != http.StatusAccepted || !afterRestart.Replayed || afterRestart.Mode != outbound.ModeAsync {
			t.Fatalf("unknown effect changed after restart: %d %+v", status, afterRestart)
		}
		if captures := gateway.getCaptures(t); len(captures) != before+1 || captures[before].ID != capture.ID {
			t.Fatalf("recovery duplicated recipient effect: before=%d captures=%+v", before, captures)
		}
	})
}
