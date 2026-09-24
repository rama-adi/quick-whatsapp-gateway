package main

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func runE2ESendFaults(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	media := base64.StdEncoding.EncodeToString([]byte("isolated media bytes"))
	cases := []struct {
		name, mode string
		request    domain.SendRequest
	}{
		{name: "send network error", mode: "send_error", request: domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "failed network send"}},
		{name: "upload network error", mode: "upload_error", request: domain.SendRequest{
			Type: domain.SendTypeImage, To: e2eGroupJID,
			Media: &domain.MediaPayload{Data: media, Mimetype: "image/png"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name+" then healthy send", func(t *testing.T) {
			before := len(gateway.getCaptures(t))
			gateway.fault(t, tc.mode)
			status, _ := infra.send(t, e2eOrgAKey, "fault-"+tc.mode, tc.request)
			if status == http.StatusOK || status == http.StatusAccepted {
				t.Fatalf("%s falsely acknowledged send", tc.mode)
			}
			if captures := gateway.getCaptures(t); len(captures) != before {
				t.Fatalf("%s leaked WhatsApp send: before=%d after=%d", tc.mode, before, len(captures))
			}
			gateway.fault(t, "none")
			ctx, cancel := e2eContext(t)
			defer cancel()
			var recoveredID string
			e2eEventually(t, ctx, tc.mode+" outbox recovery", func() bool {
				var state string
				if err := infra.db.QueryRowContext(ctx, `SELECT status,wa_message_id FROM outbox
					WHERE organization_id=? AND idempotency_key=?`,
					e2eOrgA, "fault-"+tc.mode).Scan(&state, &recoveredID); err != nil {
					return false
				}
				return state == "sent" && recoveredID != ""
			})
			if captures := gateway.waitCaptureCount(t, before+1); captures[before].ID != recoveredID {
				t.Fatalf("%s recovery capture = %+v; outbox WA ID=%q", tc.mode, captures[before], recoveredID)
			}
			tc.request.Text = "healthy after " + tc.mode
			status, result := infra.send(t, e2eOrgAKey, "healthy-after-"+tc.mode, tc.request)
			if status != http.StatusOK || result.WAMessageID == "" {
				t.Fatalf("healthy send after %s: %d %+v", tc.mode, status, result)
			}
			if captures := gateway.waitCaptureCount(t, before+2); captures[before+1].ID != result.WAMessageID {
				t.Fatalf("healthy capture after %s = %+v", tc.mode, captures[before+1])
			}
		})
	}
	t.Run("invalid send rejected before WhatsApp", func(t *testing.T) {
		before := len(gateway.getCaptures(t))
		invalid := []domain.SendRequest{
			{Type: domain.SendTypeText, To: e2eGroupJID},
			{Type: domain.SendTypeImage, To: e2eGroupJID},
			{Type: "unsupported", To: e2eGroupJID, Text: "bad type"},
		}
		for index, request := range invalid {
			status, _ := infra.send(t, e2eOrgAKey, "invalid-send-"+string(rune('a'+index)), request)
			if status < http.StatusBadRequest {
				t.Fatalf("invalid request %d accepted with %d", index, status)
			}
		}
		if captures := gateway.getCaptures(t); len(captures) != before {
			t.Fatalf("invalid requests reached WhatsApp: before=%d after=%d", before, len(captures))
		}
	})
}
