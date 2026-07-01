package outbound

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
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

func TestImageMetadata_WideImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 80, B: 140, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	width, height, thumb := imageMetadata(buf.Bytes())
	if width != 320 || height != 120 {
		t.Fatalf("dimensions = %dx%d, want 320x120", width, height)
	}
	if len(thumb) == 0 {
		t.Fatal("thumbnail is empty")
	}
}
