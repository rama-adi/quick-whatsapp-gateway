package main

import (
	"net/http"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
)

func runE2EMessageOperations(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	const otherGroup = "120363000000000002@g.us"
	base := "/api/v1/sessions/" + e2eSessionID + "/messages/"
	status, source := infra.send(t, e2eOrgAKey, "message-ops-source", domain.SendRequest{
		Type: domain.SendTypeText, To: e2eGroupJID, Text: "original message",
	})
	if status != http.StatusOK || source.WAMessageID == "" {
		t.Fatalf("message operations source: %d %+v", status, source)
	}
	operations := []struct {
		name, method, path string
		body               map[string]any
		check              func(*waE2E.Message) bool
	}{
		{name: "edit", method: http.MethodPatch, path: base + source.WAMessageID,
			body: map[string]any{"chat": e2eGroupJID, "text": "edited message"},
			check: func(m *waE2E.Message) bool {
				return m.GetEditedMessage().GetMessage().GetProtocolMessage().GetEditedMessage().GetConversation() == "edited message"
			}},
		{name: "add reaction", method: http.MethodPost, path: base + source.WAMessageID + "/reaction",
			body:  map[string]any{"chat": e2eGroupJID, "emoji": "👍"},
			check: func(m *waE2E.Message) bool { return m.GetReactionMessage().GetText() == "👍" }},
		{name: "remove reaction", method: http.MethodDelete, path: base + source.WAMessageID + "/reaction",
			body: map[string]any{"chat": e2eGroupJID},
			check: func(m *waE2E.Message) bool {
				return m.GetReactionMessage() != nil && m.GetReactionMessage().GetText() == ""
			}},
		{name: "forward", method: http.MethodPost, path: base + source.WAMessageID + "/forward",
			body:  map[string]any{"chat": e2eGroupJID, "to": otherGroup},
			check: func(m *waE2E.Message) bool { return m.GetExtendedTextMessage().GetContextInfo().GetIsForwarded() }},
		{name: "revoke", method: http.MethodDelete, path: base + source.WAMessageID,
			body:  map[string]any{"chat": e2eGroupJID},
			check: func(m *waE2E.Message) bool { return m.GetProtocolMessage() != nil }},
	}
	for _, op := range operations {
		t.Run("message operation "+op.name, func(t *testing.T) {
			before := len(gateway.getCaptures(t))
			var result outbound.SendResult
			status := infra.request(t, op.method, op.path, e2eOrgAKey, op.body, &result, nil)
			if status != http.StatusOK || result.WAMessageID == "" {
				t.Fatalf("%s returned %d %+v", op.name, status, result)
			}
			capture := gateway.waitCaptureCount(t, before+1)[before]
			if capture.ID != result.WAMessageID {
				t.Fatalf("%s capture/result mismatch: %+v %+v", op.name, capture, result)
			}
			if op.name == "forward" && capture.To != otherGroup {
				t.Fatalf("forward destination = %s", capture.To)
			}
			var message waE2E.Message
			if err := protojson.Unmarshal(capture.Message, &message); err != nil {
				t.Fatal(err)
			}
			if !op.check(&message) {
				t.Fatalf("%s WhatsApp payload = %+v", op.name, &message)
			}
		})
	}
	status, poll := infra.send(t, e2eOrgAKey, "message-ops-poll", domain.SendRequest{
		Type: domain.SendTypePoll, To: e2eGroupJID, Name: "Vote?", Options: []string{"A", "B"}, SelectableCount: 1,
	})
	if status != http.StatusOK || poll.WAMessageID == "" {
		t.Fatalf("poll source returned %d %+v", status, poll)
	}
	t.Run("message operation vote", func(t *testing.T) {
		before := len(gateway.getCaptures(t))
		var result outbound.SendResult
		status := infra.request(t, http.MethodPost, base+poll.WAMessageID+"/vote", e2eOrgAKey,
			map[string]any{"chat": e2eGroupJID, "sender": e2eDeviceLID, "options": []string{"A"}}, &result, nil)
		if status != http.StatusOK || result.WAMessageID == "" {
			t.Fatalf("vote returned %d %+v; API log: %s", status, result, infra.apiOutput.String())
		}
		capture := gateway.waitCaptureCount(t, before+1)[before]
		var message waE2E.Message
		if err := protojson.Unmarshal(capture.Message, &message); err != nil {
			t.Fatal(err)
		}
		key := message.GetPollUpdateMessage().GetPollCreationMessageKey()
		if capture.ID != result.WAMessageID || key.GetID() != poll.WAMessageID ||
			key.GetParticipant() != e2eDeviceLID || !key.GetFromMe() {
			t.Fatalf("vote capture ID=%q result ID=%q poll key ID=%q participant=%q fromMe=%t",
				capture.ID, result.WAMessageID, key.GetID(), key.GetParticipant(), key.GetFromMe())
		}
	})
}
