package outbound

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func TestFillOwnQuoteParticipant(t *testing.T) {
	group := types.NewJID("120363123456789012", types.GroupServer)
	dm := types.NewJID("6281234567890", types.DefaultUserServer)
	ownLID := types.JID{User: "205227043110953", Device: 12, Server: types.HiddenUserServer}
	ownPN := types.JID{User: "6287787505413", Device: 12, Server: types.DefaultUserServer}
	fromMe := QuoteInfo{ID: "MSG1", FromMe: true, Type: domain.SendTypeText, Body: "poll"}

	t.Run("group own message prefers LID, non-AD", func(t *testing.T) {
		got := fillOwnQuoteParticipant(group, fromMe, ownLID, ownPN)
		if want := "205227043110953@lid"; got.SenderJID != want {
			t.Fatalf("participant = %q, want %q", got.SenderJID, want)
		}
	})

	t.Run("group own message falls back to PN when no LID", func(t *testing.T) {
		got := fillOwnQuoteParticipant(group, fromMe, types.EmptyJID, ownPN)
		if want := "6287787505413@s.whatsapp.net"; got.SenderJID != want {
			t.Fatalf("participant = %q, want %q", got.SenderJID, want)
		}
	})

	t.Run("direct chat left empty", func(t *testing.T) {
		got := fillOwnQuoteParticipant(dm, fromMe, ownLID, ownPN)
		if got.SenderJID != "" {
			t.Fatalf("participant = %q, want empty", got.SenderJID)
		}
	})

	t.Run("other-sender quote untouched", func(t *testing.T) {
		other := QuoteInfo{ID: "MSG2", SenderJID: "111@lid", Body: "hi"}
		got := fillOwnQuoteParticipant(group, other, ownLID, ownPN)
		if got.SenderJID != "111@lid" {
			t.Fatalf("participant = %q, want 111@lid", got.SenderJID)
		}
	})

	t.Run("no identity available stays empty", func(t *testing.T) {
		got := fillOwnQuoteParticipant(group, fromMe, types.EmptyJID, types.EmptyJID)
		if got.SenderJID != "" {
			t.Fatalf("participant = %q, want empty", got.SenderJID)
		}
	})
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
