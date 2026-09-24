package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func runE2EStreamScenarios(t *testing.T, infra *e2eInfra) {
	t.Run("health metrics and published contract", func(t *testing.T) {
		for _, path := range []string{"/healthz", "/readyz", "/metrics", "/api/v1/openapi.yaml"} {
			response, err := http.Get(infra.apiURL + path)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 200 || len(body) == 0 {
				t.Fatalf("%s: status=%d body=%s", path, response.StatusCode, body)
			}
			if strings.HasSuffix(path, "openapi.yaml") && !strings.Contains(string(body), "operationId: sendMessage") {
				t.Fatal("published OpenAPI lacks send operation")
			}
		}
	})
	t.Run("realtime tickets are scoped single use and resume durable events", func(t *testing.T) {
		ctx, cancel := e2eContext(t)
		defer cancel()
		ticketPath := "/api/v1/realtime/ticket"
		scope := map[string]any{"scope": "session", "session": e2eSessionID}
		e2eRequireStatus(t, infra.request(t, "POST", ticketPath, e2eOrgBKey, scope, nil, nil), 404)
		e2eRequireStatus(t, infra.request(t, "POST", ticketPath, "", scope, nil, nil), 401)
		e2eRequireStatus(t, infra.request(t, "POST", ticketPath, e2eOrgAKey, map[string]any{"scope": "firehose"}, nil, nil), 403)
		e2eRequireStatus(t, infra.request(t, "GET", "/api/v1/realtime?ticket=forged", "", nil, nil, nil), 401)
		status, first := infra.send(t, e2eOrgAKey, "replay-cursor-first", domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID, Text: "before resume"})
		e2eRequireStatus(t, status, 200)
		status, second := infra.send(t, e2eOrgAKey, "replay-cursor-second", domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID, Text: "after resume"})
		e2eRequireStatus(t, status, 200)
		var cursor string
		if err := infra.db.QueryRowContext(ctx, `SELECT event_id FROM event_log WHERE session_id=? AND type='message.from_me' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.waMessageId'))=?`, e2eSessionID, first.WAMessageID).Scan(&cursor); err != nil {
			t.Fatal(err)
		}
		var ticket struct {
			URL string `json:"url"`
		}
		scope["since"] = cursor
		scope["events"] = []string{domain.EventMessageFromMe}
		e2eRequireStatus(t, infra.request(t, "POST", ticketPath, e2eOrgAKey, scope, &ticket, nil), 201)
		conn, _, err := websocket.Dial(ctx, ticket.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.CloseNow()
		// The connected envelope precedes durable replay and is ignored by this reader.
		event := e2eReadEvent(t, conn, domain.EventMessageFromMe)
		payload, ok := event.Payload.(map[string]any)
		if !ok || payload["waMessageId"] != second.WAMessageID || event.Organization != e2eOrgA {
			t.Fatalf("resume returned wrong event: %+v", event)
		}
		reused, response, err := websocket.Dial(ctx, ticket.URL, nil)
		if reused != nil {
			reused.CloseNow()
		}
		if err == nil || response == nil || response.StatusCode != 401 {
			t.Fatalf("reused ticket accepted: response=%+v err=%v", response, err)
		}
	})
}
