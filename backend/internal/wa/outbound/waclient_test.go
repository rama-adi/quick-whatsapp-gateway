package outbound

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

type albumDeadlineTransport struct {
	commits []string
	advance func()
}

func (f *albumDeadlineTransport) SendMessage(_ context.Context, _ types.JID, _ *waE2E.Message,
	extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	id := ""
	if len(extra) > 0 {
		id = string(extra[0].ID)
	}
	f.commits = append(f.commits, id)
	if len(f.commits) == 1 {
		f.advance()
	}
	return whatsmeow.SendResponse{ID: id, Timestamp: time.Now()}, nil
}

func (*albumDeadlineTransport) Upload(context.Context, []byte, whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return whatsmeow.UploadResponse{}, nil
}

// The process E2E clock cannot be paused between two album transport calls.
// This uses the real album builder and transport seam to establish the exact
// partial-prefix fault: a completed container followed by clock expiry.
func TestAlbumDeadlineCheckedAfterAcknowledgedPrefix(t *testing.T) {
	now := time.UnixMilli(1000)
	expiresAt := int64(2000)
	transport := &albumDeadlineTransport{advance: func() { now = time.UnixMilli(2000) }}
	client := NewWhatsmeowClientWithTransport(&whatsmeow.Client{Store: &waStore.Device{}}, transport)
	ctx := WithDispatchDeadline(WithCommandMessageIDs(context.Background(), "album-deadline"),
		&expiresAt, func() time.Time { return now })
	_, _, err := client.SendAlbum(ctx, "555@s.whatsapp.net", "", []AlbumMedia{
		{Type: domain.SendTypeImage, Data: []byte("first"), Mimetype: "image/png"},
		{Type: domain.SendTypeImage, Data: []byte("second"), Mimetype: "image/png"},
	}, QuoteInfo{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("partial album error = %v, want deadline exceeded", err)
	}
	if len(transport.commits) != 1 || transport.commits[0] != ReservedMessageIDs("album-deadline",
		domain.SendRequest{Type: domain.SendTypeAlbum, Medias: []domain.AlbumMediaPayload{{}, {}}})[0] {
		t.Fatalf("expiry allowed a child transport commit: %v", transport.commits)
	}
}

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
