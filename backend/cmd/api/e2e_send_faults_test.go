package main

import (
	"encoding/base64"
	"net/http"
	"strings"
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
	t.Run("list rejected by WhatsApp is terminal and explains buttons fallback", func(t *testing.T) {
		gateway.fault(t, "server_405")
		defer gateway.fault(t, "none")
		key := "list-server-405"
		request := domain.SendRequest{Type: domain.SendTypeList, To: e2eGroupJID, Text: "Choose", List: &domain.SelectionList{
			Title: "Choices", Sections: []domain.ListSection{{Title: "Options", Rows: []domain.ListRow{{ID: "one", Title: "One"}}}},
		}}
		var response domain.ErrorBody
		status := infra.request(t, http.MethodPost, "/api/v1/sessions/"+e2eSessionID+"/messages", e2eOrgAKey,
			request, &response, map[string]string{"Idempotency-Key": key})
		if status != http.StatusNotImplemented || response.Error == nil ||
			response.Error.Code != domain.CodeNotImplemented || response.Error.Message != "WhatsApp rejected list messages for this session (405); use buttons" {
			t.Fatalf("list rejection = status %d response %+v", status, response)
		}
		var outboxStatus string
		var terminalAt *int64
		if err := infra.db.QueryRow(`SELECT status,terminal_at FROM outbox WHERE organization_id=? AND idempotency_key=?`,
			e2eOrgA, key).Scan(&outboxStatus, &terminalAt); err != nil {
			t.Fatal(err)
		}
		if outboxStatus != string(domain.OutboxFailed) || terminalAt == nil {
			t.Fatalf("list outbox = status %q terminal %v", outboxStatus, terminalAt)
		}
	})
	t.Run("invalid send rejected before WhatsApp", func(t *testing.T) {
		before := len(gateway.getCaptures(t))
		invalid := []domain.SendRequest{
			{Type: domain.SendTypeText, To: e2eGroupJID},
			{Type: domain.SendTypeImage, To: e2eGroupJID},
			{Type: "unsupported", To: e2eGroupJID, Text: "bad type"},
			{Type: domain.SendTypeButtons, To: e2eGroupJID, Text: "Choose", Buttons: []domain.ReplyButton{
				{ID: "one", Title: "One"}, {ID: "two", Title: "Two"},
				{ID: "three", Title: "Three"}, {ID: "four", Title: "Four"},
			}},
			{Type: domain.SendTypeButtons, To: e2eGroupJID, Text: "Choose", Buttons: []domain.ReplyButton{
				{ID: "long", Title: "123456789012345678901"},
			}},
			{Type: domain.SendTypeButtons, To: e2eGroupJID, Text: "Open", Buttons: []domain.ReplyButton{
				{Kind: "url", Title: "Bad URL", URL: "javascript:alert(1)"},
			}},
			{Type: domain.SendTypeButtons, To: e2eGroupJID, Text: "Copy", Buttons: []domain.ReplyButton{
				{Kind: "copy", Title: "Copy"},
			}},
			{Type: domain.SendTypeButtons, To: e2eGroupJID, Text: "Open", Buttons: []domain.ReplyButton{
				{Kind: "url", Title: "Too long", URL: "https://example.com/" + strings.Repeat("a", 32_769)},
			}},
			{Type: domain.SendTypeButtons, To: e2eGroupJID, Text: "Copy", Buttons: []domain.ReplyButton{
				{Kind: "copy", Title: "Too long", Code: strings.Repeat("é", 16_385)},
			}},
			{Type: domain.SendTypeButtons, To: e2eGroupJID, Text: "Image", Buttons: []domain.ReplyButton{
				{ID: "one", Title: "One"},
			}, HeaderImage: &domain.MediaPayload{Mimetype: "image/png"}},
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
