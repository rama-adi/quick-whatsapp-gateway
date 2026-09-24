package main

import (
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func TestQuoteForSendRejectsMismatchedContext(t *testing.T) {
	quote := quoteForSend(domain.SendRequest{
		To: "one@g.us", ReplyTo: "incoming-id",
		QuoteContext: &domain.SendQuoteContext{ChatJID: "two@g.us", SenderJID: "private@lid", Body: "private"},
	})
	if quote.ID != "incoming-id" || quote.ChatJID != "" || quote.SenderJID != "" || quote.Body != "" {
		t.Fatalf("mismatched quote context leaked: %#v", quote)
	}
}
