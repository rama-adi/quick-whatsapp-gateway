package main

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

// engineDispatcher adapts the session-routing WAClient to the engine adapter's
// transport-independent dispatch ports. The API owns validation, idempotency,
// rate limits, and retries; this is the raw per-session whatsmeow bridge.
type engineDispatcher struct {
	client outbound.WAClient
}

func newEngineDispatcher(client outbound.WAClient) *engineDispatcher {
	return &engineDispatcher{client: client}
}

// Dispatch routes one validated send to whatsmeow.
func (d *engineDispatcher) Dispatch(ctx context.Context, req domain.SendRequest) (string, int64, error) {
	quote := quoteForSend(req)
	switch req.Type {
	case domain.SendTypeText:
		return d.client.SendText(ctx, req.To, req.Text, quote, req.Mentions)
	case domain.SendTypeButtons, domain.SendTypeList:
		return d.client.SendInteractive(ctx, req, quote)
	case domain.SendTypePoll:
		return d.client.SendPoll(ctx, req.To, req.Name, req.Options, req.SelectableCount, req.PollEndTime, req.PollHideVotes)
	case domain.SendTypeLocation:
		return d.client.SendLocation(ctx, req.To, req.Latitude, req.Longitude, req.Name)
	case domain.SendTypeContact:
		var name, phone, vcard string
		if req.Contact != nil {
			name, phone, vcard = req.Contact.Name, req.Contact.Phone, req.Contact.VCard
		}
		return d.client.SendContact(ctx, req.To, name, phone, vcard)
	case domain.SendTypeImage, domain.SendTypeVideo, domain.SendTypeAudio, domain.SendTypeDocument, domain.SendTypeSticker:
		data, err := engineMediaBytes(req.Media)
		if err != nil {
			return "", 0, err
		}
		var mimetype, caption, filename string
		if req.Media != nil {
			mimetype, caption, filename = req.Media.Mimetype, req.Media.Caption, req.Media.Filename
		}
		return d.client.SendMedia(ctx, req.To, req.Type, data, mimetype, caption, filename, quote, req.Mentions)
	case domain.SendTypeAlbum:
		medias := make([]outbound.AlbumMedia, len(req.Medias))
		for i, item := range req.Medias {
			data, err := engineMediaBytes(&domain.MediaPayload{Data: item.Data, URL: item.URL, Mimetype: item.Mimetype})
			if err != nil {
				return "", 0, fmt.Errorf("medias[%d]: %s", i, err.Error())
			}
			mediaType := item.Type
			if mediaType == "" {
				mediaType = domain.SendTypeImage
			}
			medias[i] = outbound.AlbumMedia{Type: mediaType, Data: data, Mimetype: item.Mimetype}
		}
		return d.client.SendAlbum(ctx, req.To, req.Caption, medias, quote, req.Mentions)
	default:
		return "", 0, domain.ErrValidation(fmt.Sprintf("unsupported send type %q", req.Type))
	}
}

func quoteForSend(req domain.SendRequest) outbound.QuoteInfo {
	quote := outbound.QuoteInfo{ID: req.ReplyTo}
	if req.ReplyTo == "" || req.QuoteContext == nil || req.QuoteContext.ChatJID != req.To {
		return quote
	}
	quote.ChatJID = req.QuoteContext.ChatJID
	quote.SenderJID = req.QuoteContext.SenderJID
	quote.Type = req.QuoteContext.Type
	quote.Body = req.QuoteContext.Body
	quote.FromMe = req.QuoteContext.FromMe
	return quote
}

// DispatchOp routes one validated message sub-resource operation to whatsmeow.
func (d *engineDispatcher) DispatchOp(ctx context.Context, req outbound.OpRequest) (outbound.SendResult, error) {
	var (
		waID string
		ts   int64
		err  error
	)
	switch req.Op {
	case outbound.OpReaction:
		waID, ts, err = d.client.React(ctx, req.Chat, req.Sender, req.MsgID, req.Emoji)
	case outbound.OpEdit:
		waID, ts, err = d.client.Edit(ctx, req.Chat, req.MsgID, req.NewText)
	case outbound.OpRevoke:
		waID, ts, err = d.client.Revoke(ctx, req.Chat, req.Sender, req.MsgID)
	case outbound.OpVote:
		waID, ts, err = d.client.Vote(ctx, req.Chat, req.Sender, req.MsgID, req.Options)
	case outbound.OpForward:
		waID, ts, err = d.client.Forward(ctx, req.To, req.Chat, req.Sender, req.MsgID)
	default:
		return outbound.SendResult{}, domain.ErrValidation(fmt.Sprintf("unknown message op %q", req.Op))
	}
	if err != nil {
		return outbound.SendResult{}, err
	}
	return outbound.SendResult{WAMessageID: waID, Timestamp: ts}, nil
}

// engineMediaBytes resolves base64 media payloads. The gateway no longer fetches
// URLs: the API resolves media before issuing a command.
func engineMediaBytes(media *domain.MediaPayload) ([]byte, error) {
	if media == nil || media.Data == "" {
		return nil, domain.ErrValidation("media data is required")
	}
	data, err := decodeBase64Payload(media.Data)
	if err != nil {
		return nil, domain.ErrValidation("media data must be base64")
	}
	return data, nil
}

func decodeBase64Payload(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
