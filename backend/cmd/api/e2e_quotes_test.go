package main

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
)

func runE2EQuoteVariants(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("image reply to inbound text carries text context", func(t *testing.T) {
		before := len(gateway.getCaptures(t))
		status, result := infra.send(t, e2eOrgAKey, "image-replies-text", domain.SendRequest{
			Type: domain.SendTypeImage, To: e2eGroupJID, ReplyTo: e2eTextQuoteWAID,
			Media: &domain.MediaPayload{
				Data:     base64.StdEncoding.EncodeToString([]byte("image fixture")),
				Mimetype: "image/png", Caption: "reply image",
			},
		})
		if status != http.StatusOK || result.WAMessageID == "" {
			t.Fatalf("image reply to text = %d %+v", status, result)
		}
		captures := gateway.waitCaptureCount(t, before+1)
		var message waE2E.Message
		if err := protojson.Unmarshal(captures[before].Message, &message); err != nil {
			t.Fatal(err)
		}
		context := message.GetImageMessage().GetContextInfo()
		if context.GetStanzaID() != e2eTextQuoteWAID ||
			context.GetParticipant() != e2eSenderLID ||
			context.GetQuotedMessage().GetConversation() != "text trigger" {
			t.Fatalf("image reply lost text trigger: %+v", context)
		}
	})
	t.Run("reply to own message carries own LID", func(t *testing.T) {
		before := len(gateway.getCaptures(t))
		status, first := infra.send(t, e2eOrgAKey, "own-quote-source", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "my earlier text",
		})
		if status != http.StatusOK || first.WAMessageID == "" {
			t.Fatalf("own quote source = %d %+v", status, first)
		}
		status, second := infra.send(t, e2eOrgAKey, "own-quote-reply", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "my reply", ReplyTo: first.WAMessageID,
		})
		if status != http.StatusOK || second.WAMessageID == "" {
			t.Fatalf("own quote reply = %d %+v", status, second)
		}
		captures := gateway.waitCaptureCount(t, before+2)
		var message waE2E.Message
		if err := protojson.Unmarshal(captures[before+1].Message, &message); err != nil {
			t.Fatal(err)
		}
		context := message.GetExtendedTextMessage().GetContextInfo()
		if context.GetStanzaID() != first.WAMessageID || context.GetParticipant() != e2eDeviceLID ||
			context.GetQuotedMessage().GetConversation() != "my earlier text" {
			t.Fatalf("own reply context = %+v", context)
		}
	})
	t.Run("cross chat quote does not disclose stored content", func(t *testing.T) {
		const otherGroup = "120363000000000002@g.us"
		before := len(gateway.getCaptures(t))
		status, result := infra.send(t, e2eOrgAKey, "cross-chat-quote", domain.SendRequest{
			Type: domain.SendTypeText, To: otherGroup, Text: "new chat reply", ReplyTo: e2eQuoteWAID,
		})
		if status != http.StatusOK || result.WAMessageID == "" {
			t.Fatalf("cross chat quote = %d %+v", status, result)
		}
		captures := gateway.waitCaptureCount(t, before+1)
		var message waE2E.Message
		if err := protojson.Unmarshal(captures[before].Message, &message); err != nil {
			t.Fatal(err)
		}
		context := message.GetExtendedTextMessage().GetContextInfo()
		if context.GetStanzaID() != e2eQuoteWAID || context.GetParticipant() != "" ||
			context.GetQuotedMessage() != nil {
			t.Fatalf("cross-chat content leaked: %+v", context)
		}
	})
}
