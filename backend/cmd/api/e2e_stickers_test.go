package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/coder/websocket"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func runE2EStickers(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("stickers deduplicate and expand live quoted and replay events", func(t *testing.T) {
		ctx, cancel := e2eContext(t)
		defer cancel()
		var ticket struct {
			URL string `json:"url"`
		}
		e2eRequireStatus(t, infra.request(t, "POST", "/api/v1/realtime/ticket", e2eOrgAKey, map[string]any{"scope": "session", "session": e2eSessionID, "events": []string{domain.EventMessage, domain.EventMessageFromMe}}, &ticket, nil), 201)
		conn, _, err := websocket.Dial(ctx, ticket.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.CloseNow()
		content := []byte("isolated-media")
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		sticker := &waE2E.StickerMessage{Mimetype: proto.String("image/webp"), FileSHA256: sum[:], FileLength: proto.Uint64(uint64(len(content))), URL: proto.String("https://isolated.invalid/sticker")}
		inject := func(id string, msg *waE2E.Message) {
			t.Helper()
			raw, err := protojson.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]any{"id": id, "chat": e2eGroupJID, "sender": e2eSenderLID, "message": json.RawMessage(raw)})
			req, _ := http.NewRequestWithContext(ctx, "POST", gateway.controlURL+"/incoming", bytes.NewReader(body))
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			e2eRequireStatus(t, res.StatusCode, 204)
		}
		check := func(event domain.Event, field string) {
			t.Helper()
			p, ok := event.Payload.(map[string]any)
			if !ok {
				t.Fatalf("payload: %+v", event.Payload)
			}
			s, ok := p[field].(map[string]any)
			if !ok || s["sha256"] != hash || s["base64"] != base64.StdEncoding.EncodeToString(content) || s["status"] != "available" {
				t.Fatalf("sticker event: %+v", p)
			}
		}
		inject("sticker-first", &waE2E.Message{StickerMessage: sticker})
		first := e2eReadEvent(t, conn, domain.EventMessage)
		check(first, "sticker")
		var fetched map[string]any
		stickerPath := "/api/v1/sessions/" + e2eSessionID + "/chats/" + e2eGroupJID + "/messages/sticker-first/sticker"
		e2eRequireStatus(t, infra.request(t, "GET", stickerPath, e2eOrgAKey, nil, &fetched, nil), 200)
		if fetched["sha256"] != hash || fetched["base64"] != base64.StdEncoding.EncodeToString(content) {
			t.Fatalf("sticker read mismatch: %+v", fetched)
		}
		e2eRequireStatus(t, infra.request(t, "GET", stickerPath, e2eOrgBKey, nil, nil, nil), 404)
		e2eRequireStatus(t, infra.request(t, "GET", "/api/v1/sessions/"+e2eSessionID+"/chats/foreign@g.us/messages/sticker-first/sticker", e2eOrgAKey, nil, nil, nil), 404)

		inject("sticker-again", &waE2E.Message{StickerMessage: sticker})
		check(e2eReadEvent(t, conn, domain.EventMessage), "sticker")
		// Cached content must remain available when WhatsApp downloads fail.
		gateway.fault(t, "send_error")
		defer gateway.fault(t, "none")
		// The quoted message was never captured; its content hash still resolves.
		inject("sticker-quote-inline", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("which sticker?"), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("unseen-sticker"), QuotedMessage: &waE2E.Message{StickerMessage: sticker}}}})
		check(e2eReadEvent(t, conn, domain.EventMessage), "quotedSticker")
		// Missing inline content resolves through the chat's original message reference.
		inject("sticker-quote-reference", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("again"), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("sticker-first")}}})
		check(e2eReadEvent(t, conn, domain.EventMessage), "quotedSticker")
		// An uncached download failure preserves delivery and reports unavailable content.
		unavailable := proto.Clone(sticker).(*waE2E.StickerMessage)
		unavailable.FileSHA256 = nil
		inject("sticker-unavailable", &waE2E.Message{StickerMessage: unavailable})
		failed := e2eReadEvent(t, conn, domain.EventMessage)
		failedPayload := failed.Payload.(map[string]any)
		failedSticker, ok := failedPayload["sticker"].(map[string]any)
		if !ok || failedSticker["status"] != "unavailable" || failedSticker["base64"] != nil {
			t.Fatalf("unavailable sticker lost its message or exposed content: %+v", failed)
		}
		gateway.fault(t, "none")
		status, _ := infra.send(t, e2eOrgAKey, "sticker-api-send", domain.SendRequest{Type: domain.SendTypeSticker, To: e2eGroupJID, Media: &domain.MediaPayload{Data: base64.StdEncoding.EncodeToString(content), Mimetype: "image/webp"}})
		e2eRequireStatus(t, status, 200)
		check(e2eReadEvent(t, conn, domain.EventMessageFromMe), "sticker")
		var count int
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sticker_blobs WHERE sha256=?`, hash).Scan(&count); err != nil || count != 1 {
			t.Fatalf("deduplicated blobs: %d %v", count, err)
		}
		var persisted string
		if err := infra.db.QueryRowContext(ctx, `SELECT payload FROM event_log WHERE event_id=?`, first.ID).Scan(&persisted); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains([]byte(persisted), []byte(`"base64"`)) {
			t.Fatalf("binary duplicated in event log: %s", persisted)
		}
		e2eRequireStatus(t, infra.request(t, "POST", "/api/v1/realtime/ticket", e2eOrgAKey, map[string]any{"scope": "session", "session": e2eSessionID, "since": first.ID, "events": []string{domain.EventMessage}}, &ticket, nil), 201)
		replay, _, err := websocket.Dial(ctx, ticket.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer replay.CloseNow()
		check(e2eReadEvent(t, replay, domain.EventMessage), "sticker")
	})
}
