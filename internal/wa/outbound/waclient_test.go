package outbound

import (
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestBuildContextInfo_Quote(t *testing.T) {
	ci := buildContextInfo(QuoteInfo{
		ID:        "3A39B767976D4B5D4766",
		ChatJID:   "107082225311887@lid",
		SenderJID: "107082225311887@lid",
		Type:      domain.SendTypeText,
		Body:      "quoted body",
	}, nil)
	if ci == nil {
		t.Fatal("context info is nil")
	}
	if ci.GetStanzaID() != "3A39B767976D4B5D4766" {
		t.Fatalf("stanza id = %q", ci.GetStanzaID())
	}
	if ci.GetRemoteJID() != "107082225311887@lid" {
		t.Fatalf("remote jid = %q", ci.GetRemoteJID())
	}
	if ci.GetParticipant() != "107082225311887@lid" {
		t.Fatalf("participant = %q", ci.GetParticipant())
	}
	if ci.GetQuotedMessage().GetConversation() != "quoted body" {
		t.Fatalf("quoted body = %q", ci.GetQuotedMessage().GetConversation())
	}
}
