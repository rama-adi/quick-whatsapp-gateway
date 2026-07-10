package outbound

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"

	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// whatsmeowAdapter implements WAClient over one live *whatsmeow.Client,
// translating domain-level calls into protobuf messages and SendMessage. It is
// intentionally stateless beyond that client pointer: whatsmeow owns connection
// concurrency, while each call propagates context cancellation through optional
// media fetch/upload and the final send.
//
// This file is the package's protocol boundary. Parsing or local payload-building
// failures occur before SendMessage and return no message ID; after WhatsApp
// acknowledges, send normalizes the assigned ID and server timestamp for durable
// idempotency and history recording by Sender.
type whatsmeowAdapter struct {
	cli *whatsmeow.Client
}

// NewWhatsmeowClient wraps a *whatsmeow.Client as a WAClient.
func NewWhatsmeowClient(cli *whatsmeow.Client) WAClient {
	return &whatsmeowAdapter{cli: cli}
}

// send is the shared tail: dispatch a built message and normalize the response
// into (waMessageID, epoch-ms timestamp).
func (a *whatsmeowAdapter) send(ctx context.Context, to types.JID, msg *waE2E.Message) (string, int64, error) {
	resp, err := a.cli.SendMessage(ctx, to, msg)
	if err != nil {
		return "", 0, fmt.Errorf("whatsmeow send: %w", err)
	}
	return resp.ID, resp.Timestamp.UnixMilli(), nil
}

func parseJID(s string) (types.JID, error) {
	jid, err := types.ParseJID(s)
	if err != nil {
		return types.EmptyJID, fmt.Errorf("parse jid %q: %w", s, err)
	}
	return jid, nil
}

// parseSenderJID parses an optional sender JID; "" maps to types.EmptyJID, which
// whatsmeow's Build* helpers accept for your own outgoing messages.
func parseSenderJID(s string) (types.JID, error) {
	if s == "" {
		return types.EmptyJID, nil
	}
	return parseJID(s)
}

func (a *whatsmeowAdapter) SendText(ctx context.Context, to, text string, quote QuoteInfo, mentions []string) (string, int64, error) {
	toJID, err := parseJID(to)
	if err != nil {
		return "", 0, err
	}

	// A plain Conversation suffices when there is no reply context and no
	// mentions; otherwise use ExtendedTextMessage so we can attach ContextInfo.
	if quote.Empty() && len(mentions) == 0 {
		return a.send(ctx, toJID, &waE2E.Message{Conversation: proto.String(text)})
	}

	ctxInfo := buildContextInfo(a.fillOwnQuote(toJID, quote), mentions)
	return a.send(ctx, toJID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: ctxInfo,
		},
	})
}

func (a *whatsmeowAdapter) SendPoll(ctx context.Context, to, name string, options []string, selectableCount int, endTime int64, hideVotes bool) (string, int64, error) {
	toJID, err := parseJID(to)
	if err != nil {
		return "", 0, err
	}
	if selectableCount < 0 || selectableCount > len(options) {
		selectableCount = 0
	}
	pollOptions := make([]*waE2E.PollCreationMessage_Option, len(options))
	for i, option := range options {
		pollOptions[i] = &waE2E.PollCreationMessage_Option{OptionName: proto.String(option)}
	}
	poll := &waE2E.PollCreationMessage{
		Name:                   proto.String(name),
		Options:                pollOptions,
		SelectableOptionsCount: proto.Uint32(uint32(selectableCount)),
	}
	if endTime > 0 {
		poll.EndTime = proto.Int64(endTime)
	}
	if hideVotes {
		poll.HideParticipantName = proto.Bool(true)
	}
	msg := &waE2E.Message{
		PollCreationMessage: poll,
		MessageContextInfo: &waE2E.MessageContextInfo{
			MessageSecret: random.Bytes(32),
		},
	}
	return a.send(ctx, toJID, msg)
}

func (a *whatsmeowAdapter) SendLocation(ctx context.Context, to string, lat, lon float64, name string) (string, int64, error) {
	toJID, err := parseJID(to)
	if err != nil {
		return "", 0, err
	}
	loc := &waE2E.LocationMessage{
		DegreesLatitude:  proto.Float64(lat),
		DegreesLongitude: proto.Float64(lon),
	}
	if name != "" {
		loc.Name = proto.String(name)
	}
	return a.send(ctx, toJID, &waE2E.Message{LocationMessage: loc})
}

func (a *whatsmeowAdapter) SendContact(ctx context.Context, to, name, phone, vcard string) (string, int64, error) {
	toJID, err := parseJID(to)
	if err != nil {
		return "", 0, err
	}
	if vcard == "" {
		vcard = buildVCard(name, phone)
	}
	return a.send(ctx, toJID, &waE2E.Message{
		ContactMessage: &waE2E.ContactMessage{
			DisplayName: proto.String(name),
			Vcard:       proto.String(vcard),
		},
	})
}

// buildVCard assembles a minimal vCard 3.0 from a name and phone number.
func buildVCard(name, phone string) string {
	return fmt.Sprintf("BEGIN:VCARD\nVERSION:3.0\nFN:%s\nTEL;type=CELL;type=VOICE;waid=%s:%s\nEND:VCARD",
		name, phone, phone)
}

// SendMedia uploads media bytes to WhatsApp and sends the matching message kind
// (image/video/audio/document/sticker). mediaType is a domain.SendType* constant.
// mimetype is detected from the bytes when empty; caption applies to image/video/
// document, filename to document; replyTo/mentions become ContextInfo.
func (a *whatsmeowAdapter) SendMedia(ctx context.Context, to, mediaType string, data []byte, mimetype, caption, filename string, quote QuoteInfo, mentions []string) (string, int64, error) {
	toJID, err := parseJID(to)
	if err != nil {
		return "", 0, err
	}
	if mimetype == "" {
		mimetype = http.DetectContentType(data)
	}
	up, err := a.cli.Upload(ctx, data, uploadMediaType(mediaType))
	if err != nil {
		return "", 0, fmt.Errorf("whatsmeow upload %s: %w", mediaType, err)
	}
	ctxInfo := buildContextInfo(a.fillOwnQuote(toJID, quote), mentions)

	var msg *waE2E.Message
	switch mediaType {
	case domain.SendTypeImage:
		m := &waE2E.ImageMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), Mimetype: proto.String(mimetype),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), ContextInfo: ctxInfo,
		}
		if width, height, thumb := imageMetadata(data); width > 0 && height > 0 {
			m.Width = proto.Uint32(width)
			m.Height = proto.Uint32(height)
			m.JPEGThumbnail = thumb
		}
		if caption != "" {
			m.Caption = proto.String(caption)
		}
		msg = &waE2E.Message{ImageMessage: m}
	case domain.SendTypeVideo:
		m := &waE2E.VideoMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), Mimetype: proto.String(mimetype),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), ContextInfo: ctxInfo,
		}
		if caption != "" {
			m.Caption = proto.String(caption)
		}
		msg = &waE2E.Message{VideoMessage: m}
	case domain.SendTypeAudio:
		msg = &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), Mimetype: proto.String(mimetype),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), ContextInfo: ctxInfo,
		}}
	case domain.SendTypeDocument:
		m := &waE2E.DocumentMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), Mimetype: proto.String(mimetype),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), ContextInfo: ctxInfo,
		}
		if filename != "" {
			m.FileName = proto.String(filename)
			m.Title = proto.String(filename)
		}
		if caption != "" {
			m.Caption = proto.String(caption)
		}
		msg = &waE2E.Message{DocumentMessage: m}
	case domain.SendTypeSticker:
		msg = &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), Mimetype: proto.String(mimetype),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), ContextInfo: ctxInfo,
		}}
	default:
		return "", 0, domain.ErrValidation(fmt.Sprintf("unsupported media type %q", mediaType))
	}
	return a.send(ctx, toJID, msg)
}

func imageMetadata(data []byte) (width, height uint32, jpegThumbnail []byte) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, nil
	}
	thumb := data
	if len(data) > 64*1024 {
		return uint32(cfg.Width), uint32(cfg.Height), nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return uint32(cfg.Width), uint32(cfg.Height), nil
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75}); err != nil {
		return uint32(cfg.Width), uint32(cfg.Height), nil
	}
	thumb = buf.Bytes()
	if len(thumb) > 64*1024 {
		thumb = nil
	}
	return uint32(cfg.Width), uint32(cfg.Height), thumb
}

// uploadMediaType maps a domain media send type onto whatsmeow's upload key set.
// Stickers upload under the image key set (whatsmeow's classToMediaType).
func uploadMediaType(mediaType string) whatsmeow.MediaType {
	switch mediaType {
	case domain.SendTypeVideo:
		return whatsmeow.MediaVideo
	case domain.SendTypeAudio:
		return whatsmeow.MediaAudio
	case domain.SendTypeDocument:
		return whatsmeow.MediaDocument
	default: // image, sticker
		return whatsmeow.MediaImage
	}
}

// fillOwnQuote resolves the participant for a quoted message this session sent
// itself. Group replies require an explicit participant — when it is missing,
// receiving clients attribute the quote to the wrong author — so fill in the
// session's own identity from the device store. Direct chats are left alone:
// there an empty participant already means "own message".
func (a *whatsmeowAdapter) fillOwnQuote(to types.JID, quote QuoteInfo) QuoteInfo {
	return fillOwnQuoteParticipant(to, quote, a.cli.Store.GetLID(), a.cli.Store.GetJID())
}

// fillOwnQuoteParticipant is the pure core of fillOwnQuote. The LID is
// preferred (modern groups are lid-addressed), falling back to the phone-number
// JID for sessions that predate LID assignment.
func fillOwnQuoteParticipant(to types.JID, quote QuoteInfo, ownLID, ownJID types.JID) QuoteInfo {
	if !quote.FromMe || quote.SenderJID != "" || to.Server != types.GroupServer {
		return quote
	}
	own := ownLID
	if own.IsEmpty() {
		own = ownJID
	}
	if !own.IsEmpty() {
		quote.SenderJID = own.ToNonAD().String()
	}
	return quote
}

// buildContextInfo assembles reply + mention context, or nil when neither is set.
func buildContextInfo(quote QuoteInfo, mentions []string) *waE2E.ContextInfo {
	if quote.Empty() && len(mentions) == 0 {
		return nil
	}
	ci := &waE2E.ContextInfo{}
	if !quote.Empty() {
		ci.StanzaID = proto.String(quote.ID)
		if quote.ChatJID != "" {
			ci.RemoteJID = proto.String(quote.ChatJID)
		}
		if quote.SenderJID != "" {
			ci.Participant = proto.String(quote.SenderJID)
		}
		if quoted := quotedMessageProto(quote); quoted != nil {
			ci.QuotedMessage = quoted
		}
	}
	if len(mentions) > 0 {
		ci.MentionedJID = mentions
	}
	return ci
}

func quotedMessageProto(quote QuoteInfo) *waE2E.Message {
	if quote.Body == "" {
		return nil
	}
	switch quote.Type {
	case domain.SendTypeImage:
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String(quote.Body)}}
	case domain.SendTypeVideo:
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String(quote.Body)}}
	case domain.SendTypeDocument:
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: proto.String(quote.Body)}}
	default:
		return &waE2E.Message{Conversation: proto.String(quote.Body)}
	}
}

func (a *whatsmeowAdapter) React(ctx context.Context, chat, sender, msgID, emoji string) (string, int64, error) {
	chatJID, err := parseJID(chat)
	if err != nil {
		return "", 0, err
	}
	senderJID, err := parseSenderJID(sender)
	if err != nil {
		return "", 0, err
	}
	msg := a.cli.BuildReaction(chatJID, senderJID, msgID, emoji)
	return a.send(ctx, chatJID, msg)
}

func (a *whatsmeowAdapter) Edit(ctx context.Context, chat, msgID, newText string) (string, int64, error) {
	chatJID, err := parseJID(chat)
	if err != nil {
		return "", 0, err
	}
	msg := a.cli.BuildEdit(chatJID, msgID, &waE2E.Message{Conversation: proto.String(newText)})
	return a.send(ctx, chatJID, msg)
}

func (a *whatsmeowAdapter) Revoke(ctx context.Context, chat, sender, msgID string) (string, int64, error) {
	chatJID, err := parseJID(chat)
	if err != nil {
		return "", 0, err
	}
	senderJID, err := parseSenderJID(sender)
	if err != nil {
		return "", 0, err
	}
	msg := a.cli.BuildRevoke(chatJID, senderJID, msgID)
	return a.send(ctx, chatJID, msg)
}

func (a *whatsmeowAdapter) Vote(ctx context.Context, pollChat, pollSender, pollMsgID string, options []string) (string, int64, error) {
	chatJID, err := parseJID(pollChat)
	if err != nil {
		return "", 0, err
	}
	senderJID, err := parseSenderJID(pollSender)
	if err != nil {
		return "", 0, err
	}
	// BuildPollVote needs the poll's MessageInfo to derive the encryption key.
	pollInfo := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chatJID, Sender: senderJID},
		ID:            pollMsgID,
	}
	msg, err := a.cli.BuildPollVote(ctx, pollInfo, options)
	if err != nil {
		return "", 0, fmt.Errorf("whatsmeow build poll vote: %w", err)
	}
	return a.send(ctx, chatJID, msg)
}

func (a *whatsmeowAdapter) Forward(ctx context.Context, to, sourceChat, sourceSender, sourceMsgID string) (string, int64, error) {
	toJID, err := parseJID(to)
	if err != nil {
		return "", 0, err
	}
	// whatsmeow has no Build-forward helper; forwarding requires the original
	// message content, which the send pipeline does not carry. We send an
	// extended-text message tagged as forwarded that references the source. A
	// richer forward (re-uploading media, copying the original body) is future
	// work, once a fetch-message-by-id path exists — see outbound-pipeline.md.
	return a.send(ctx, toJID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(""),
			ContextInfo: &waE2E.ContextInfo{
				IsForwarded:     proto.Bool(true),
				ForwardingScore: proto.Uint32(1),
				StanzaID:        proto.String(sourceMsgID),
				RemoteJID:       proto.String(sourceChat),
			},
		},
	})
}
