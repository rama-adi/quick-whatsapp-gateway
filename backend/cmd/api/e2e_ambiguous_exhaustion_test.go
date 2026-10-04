package main

import (
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

// This fault crosses the public API, real MySQL outbox, private engine RPC,
// gateway command journal, and a stateful WhatsApp recipient. The test process
// configures one permitted attempt to reach the exhaustion boundary directly.
func runE2EAmbiguousExhaustion(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("attempt exhaustion without a private dispatch is failed", func(t *testing.T) {
		const key = "no-dispatch-exhaustion"
		infra.stopAPI()
		infra.exhaustAmbiguous = true
		infra.startAPI(t)
		before := len(gateway.getCaptures(t))
		var endpoint string
		if err := infra.db.QueryRow(`SELECT grpc_endpoint FROM gateways WHERE id=?`, gateway.enrollment.id).
			Scan(&endpoint); err != nil || endpoint == "" {
			t.Fatalf("read fixture engine endpoint: %q %v", endpoint, err)
		}
		if _, err := infra.db.Exec(`UPDATE gateways SET grpc_endpoint=NULL WHERE id=?`, gateway.enrollment.id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = infra.db.Exec(`UPDATE gateways SET grpc_endpoint=? WHERE id=?`, endpoint, gateway.enrollment.id)
		})
		ctx, cancel := e2eContext(t)
		defer cancel()
		if _, err := store.NewGatewayRepo(infra.db).ResolveSessionEngineTarget(ctx, e2eOrgA, e2eSessionID); err == nil {
			t.Fatal("fixture still exposes a private engine target")
		}
		status, _ := infra.send(t, e2eOrgAKey, key, domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "never dispatched",
		})
		if status == http.StatusOK || status == http.StatusAccepted {
			t.Fatalf("unavailable send reported completion: %d", status)
		}
		var state string
		var possible bool
		if err := infra.db.QueryRow(`SELECT status,effect_possible FROM outbox WHERE organization_id=? AND idempotency_key=?`,
			e2eOrgA, key).Scan(&state, &possible); err != nil || state != string(domain.OutboxFailed) || possible {
			t.Fatalf("no-dispatch exhaustion = %s possible=%t err=%v", state, possible, err)
		}
		if got := len(gateway.getCaptures(t)); got != before {
			t.Fatalf("no-dispatch command reached recipient: %d->%d", before, got)
		}
	})

	t.Run("remote commit with every acknowledgement lost stays unknown after attempt exhaustion", func(t *testing.T) {
		const key = "ambiguous-exhaustion-one-effect"
		infra.stopAPI()
		infra.exhaustAmbiguous = true
		infra.startAPI(t)
		before := len(gateway.getCaptures(t))
		gateway.fault(t, "drop_response")
		expiresAt := time.Now().Add(5 * time.Second).UnixMilli()
		request := domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID,
			Text: "one remote effect", ExpiresAt: &expiresAt}
		status, initial := infra.send(t, e2eOrgAKey, key, request)
		if status == http.StatusOK || status == http.StatusAccepted || status == http.StatusBadRequest {
			t.Fatalf("lost acknowledgement was not admitted as an uncertain effect: %d %+v", status, initial)
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
		if status != http.StatusOK || replay.Mode != outbound.ModeSync ||
			!replay.Replayed || replay.Status != domain.MessageStatus(domain.OutboxUnknown) || replay.WAMessageID != "" {
			t.Fatalf("unknown effect replayed as definite: %d %+v", status, replay)
		}
		if len(replay.ReservedMessageIDs) != 1 || replay.ReservedMessageIDs[0] != capture.ID {
			t.Fatalf("unknown effect lost its reserved native identity: %+v actual %s", replay, capture.ID)
		}
		if wait := time.Until(time.UnixMilli(expiresAt)); wait > 0 {
			time.Sleep(wait)
		}
		lookupStatus, lookup := e2eOutboxLookup(t, infra, e2eSessionID, key, e2eOrgAKey)
		if lookupStatus != http.StatusOK || lookup.Mode != outbound.ModeSync ||
			lookup.Status != domain.MessageUnknown || lookup.OutboxID != replay.OutboxID ||
			len(lookup.ReservedMessageIDs) != 1 || lookup.ReservedMessageIDs[0] != capture.ID {
			t.Fatalf("expired unknown lookup = %d %+v", lookupStatus, lookup)
		}
		gateway.fault(t, "none")
		infra.stopAPI()
		infra.exhaustAmbiguous = false
		infra.startAPI(t)
		status, afterRestart := infra.send(t, e2eOrgAKey, key, request)
		if status != http.StatusOK || !afterRestart.Replayed || afterRestart.Mode != outbound.ModeSync ||
			afterRestart.Status != domain.MessageStatus(domain.OutboxUnknown) {
			t.Fatalf("unknown effect changed after restart: %d %+v", status, afterRestart)
		}
		if captures := gateway.getCaptures(t); len(captures) != before+1 || captures[before].ID != capture.ID {
			t.Fatalf("recovery duplicated recipient effect: before=%d captures=%+v", before, captures)
		}
	})
}
