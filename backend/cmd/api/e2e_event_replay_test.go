package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp"
)

func runE2ELostEventAck(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("lost committed event acknowledgement replays one inbound projection", func(t *testing.T) {
		const inboundID = "e2e-inbound-lost-ack"
		gateway.fault(t, "drop_event_ack")
		body, err := json.Marshal(map[string]any{
			"id":         inboundID,
			"chat":       e2eGroupJID,
			"sender":     e2eSenderLID,
			"sender_alt": "6282222222222@s.whatsapp.net",
			"from_me":    false,
			"message":    map[string]any{"conversation": "event replay after lost ack"},
		})
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(gateway.fakeURL+"/v1/numbers/"+e2eFakeNumber+"/messages", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("inject inbound message: HTTP %d", response.StatusCode)
		}
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "committed inbound event after lost ack", func() bool {
			var messages, events int
			if err := infra.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
				e2eSessionID, inboundID,
			).Scan(&messages); err != nil {
				return false
			}
			if err := infra.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM event_log WHERE organization_id=? AND session_id=?
				 AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.waMessageId'))=?`,
				e2eOrgA, e2eSessionID, inboundID,
			).Scan(&events); err != nil {
				return false
			}
			if messages > 1 || events > 1 {
				t.Fatalf("event replay duplicated messages=%d events=%d", messages, events)
			}
			return messages == 1 && events == 1
		})
		status, messages := infra.messages(t, e2eOrgAKey, e2eGroupJID)
		if status != http.StatusOK {
			t.Fatalf("history HTTP %d", status)
		}
		var matches int
		for _, message := range messages {
			if message.WAMessageID == inboundID && !message.FromMe {
				matches++
			}
		}
		if matches != 1 {
			t.Fatalf("inbound replay history matches=%d", matches)
		}
	})
}

// Audio recording presence uses a string media field, unlike message media.
// A lost ACK must replay it without blocking the following inbound message.
func runE2EAudioPresenceReplay(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("audio presence survives lost acknowledgement and subsequent inbound delivery", func(t *testing.T) {
		ws := infra.realtimeSession(t, e2eOrgAKey)
		defer ws.CloseNow()
		gateway.fault(t, "drop_event_ack")
		var state fakewhatsapp.State
		fakeModeRequest(t, gateway.fakeURL+"/v1/state", nil, &state)
		var key string
		for _, session := range state.Sessions {
			if session.Number == e2eFakeNumber && session.Connected {
				key = session.Key
			}
		}
		if key == "" {
			t.Fatal("connected fake phone missing")
		}
		fakeModeRequest(t, gateway.fakeURL+"/v1/sessions/"+key+"/events", []map[string]any{{
			"kind": "chat_presence", "chat": e2eGroupJID, "sender": e2eSenderLID,
			"data": map[string]any{"state": "composing", "media": "audio"},
		}}, nil)
		const messageID = "after-audio-presence"
		body, err := json.Marshal(map[string]any{
			"id": messageID, "chat": e2eGroupJID, "sender": e2eSenderLID,
			"message": map[string]any{"conversation": "message after recording presence"},
		})
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(gateway.fakeURL+"/v1/numbers/"+e2eFakeNumber+"/messages", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		e2eRequireStatus(t, response.StatusCode, http.StatusOK)
		observed := e2eReadEvent(t, ws, domain.EventPresenceUpdate)
		payload, ok := observed.Payload.(map[string]any)
		if !ok || payload["media"] != "audio" {
			t.Fatalf("audio presence changed: %+v", observed)
		}
		incoming := e2eReadEvent(t, ws, domain.EventMessage)
		payload, ok = incoming.Payload.(map[string]any)
		if !ok || payload["waMessageId"] != messageID {
			t.Fatalf("following inbound event missing: %+v", incoming)
		}
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "following message available through history", func() bool {
			status, messages := infra.messages(t, e2eOrgAKey, e2eGroupJID)
			if status != http.StatusOK {
				return false
			}
			for _, message := range messages {
				if message.WAMessageID == messageID {
					return !message.FromMe
				}
			}
			return false
		})
		var count int
		if err := infra.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM event_log WHERE event_id=?", observed.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("replayed presence stored %d times", count)
		}
		t.Logf("presence=%s preserved media=audio exactly once; following message=%s received through WebSocket and HTTP", observed.ID, messageID)
	})
}
