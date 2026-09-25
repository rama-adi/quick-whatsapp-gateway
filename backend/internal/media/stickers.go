package media

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func stickerContext(msg *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case msg.GetExtendedTextMessage() != nil:
		return msg.GetExtendedTextMessage().GetContextInfo()
	case msg.GetImageMessage() != nil:
		return msg.GetImageMessage().GetContextInfo()
	case msg.GetVideoMessage() != nil:
		return msg.GetVideoMessage().GetContextInfo()
	case msg.GetAudioMessage() != nil:
		return msg.GetAudioMessage().GetContextInfo()
	case msg.GetDocumentMessage() != nil:
		return msg.GetDocumentMessage().GetContextInfo()
	case msg.GetLocationMessage() != nil:
		return msg.GetLocationMessage().GetContextInfo()
	case msg.GetContactMessage() != nil:
		return msg.GetContactMessage().GetContextInfo()
	case msg.GetPollCreationMessage() != nil:
		return msg.GetPollCreationMessage().GetContextInfo()
	case msg.GetPollCreationMessageV2() != nil:
		return msg.GetPollCreationMessageV2().GetContextInfo()
	case msg.GetPollCreationMessageV3() != nil:
		return msg.GetPollCreationMessageV3().GetContextInfo()
	case msg.GetStickerMessage() != nil:
		return msg.GetStickerMessage().GetContextInfo()
	case msg.GetButtonsResponseMessage() != nil:
		return msg.GetButtonsResponseMessage().GetContextInfo()
	case msg.GetListResponseMessage() != nil:
		return msg.GetListResponseMessage().GetContextInfo()
	case msg.GetInteractiveResponseMessage() != nil:
		return msg.GetInteractiveResponseMessage().GetContextInfo()
	}
	return nil
}

func (s *Service) captureStickers(ctx context.Context, tx storedb.DBTX, e store.GatewayEvent) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return nil, err
	}
	var identity apitypes.MessagePayload
	if err := json.Unmarshal(e.Payload, &identity); err != nil {
		return nil, err
	}
	if identity.Type != domain.SendTypeSticker && identity.QuotedMessageID == "" {
		return e.Payload, nil
	}
	// Descriptors are private; only protocol-derived fields become sticker references.
	var encoded string
	_ = json.Unmarshal(payload["_mediaSource"], &encoded)
	source, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	var msg waE2E.Message
	if len(source) > 0 && !json.Valid(source) {
		if err := proto.Unmarshal(source, &msg); err != nil {
			return nil, err
		}
	}
	repo := store.NewStickerRepo(tx)
	slots := []struct {
		field, message string
		sticker        *waE2E.StickerMessage
	}{
		{field: "sticker", message: identity.WAMessageID, sticker: msg.GetStickerMessage()},
		{field: "quotedSticker", message: identity.QuotedMessageID, sticker: stickerContext(&msg).GetQuotedMessage().GetStickerMessage()},
	}
	for _, slot := range slots {
		ref, err := repo.Reference(ctx, e.SessionID, identity.ChatJID, slot.message)
		if err != nil {
			return nil, err
		}
		if slot.sticker != nil && ref == nil {
			ref, err = s.captureSticker(ctx, repo, e, slot.sticker)
			if err != nil {
				return nil, err
			}
			if ref.Status == "available" {
				if err := repo.Link(ctx, e.SessionID, identity.ChatJID, slot.message, ref.SHA256); err != nil {
					return nil, err
				}
			}
		}
		if ref != nil {
			payload[slot.field], err = json.Marshal(ref)
			if err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(payload)
}

func (s *Service) captureSticker(ctx context.Context, repo *store.StickerRepo, e store.GatewayEvent, sticker *waE2E.StickerMessage) (*apitypes.StickerData, error) {
	ref := &apitypes.StickerData{Status: "unavailable", Mimetype: "image/webp"}
	if len(sticker.GetFileSHA256()) == 32 {
		ref.SHA256 = hex.EncodeToString(sticker.GetFileSHA256())
		found, err := repo.Has(ctx, ref.SHA256)
		if err != nil {
			return nil, err
		}
		if found {
			ref.Status = "available"
			return ref, nil
		}
	}
	if s.Downloader == nil {
		return ref, nil
	}
	source, err := proto.Marshal(&waE2E.Message{StickerMessage: sticker})
	if err != nil {
		return nil, err
	}
	content, err := s.Downloader.DownloadMedia(ctx, e.OrganizationID, e.SessionID, source)
	if err != nil || len(content) == 0 {
		slog.WarnContext(ctx, "sticker content unavailable", "session", e.SessionID, "message", e.EventID, "err", err)
		return ref, nil
	}
	// Put hashes the bytes itself, never trusting a sender-supplied hash as a write key.
	hash, err := repo.Put(ctx, content)
	if err != nil {
		return nil, err
	}
	ref.SHA256 = hash
	ref.Status = "available"
	return ref, nil
}

// StickerReferences attaches stored references to API-originated events.
func (s *Service) StickerReferences(ctx context.Context, event domain.Event) (domain.Event, error) {
	raw, err := json.Marshal(event.Payload)
	if err != nil {
		return event, err
	}
	payload, err := s.captureStickers(ctx, s.DB, store.GatewayEvent{SessionID: event.Session, OrganizationID: event.Organization, Payload: raw})
	if err != nil {
		return event, fmt.Errorf("resolve sticker references: %w", err)
	}
	event.Payload = json.RawMessage(payload)
	return event, nil
}
