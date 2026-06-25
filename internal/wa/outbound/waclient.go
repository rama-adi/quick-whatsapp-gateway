package outbound

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// whatsmeowAdapter implements WAClient over a real *whatsmeow.Client, translating
// domain-level send calls into the recon §7 Build* helpers + SendMessage. This is
// the single file in the package that imports whatsmeow; Phase 3 constructs it
// per managed session and injects it into the Sender.
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

func (a *whatsmeowAdapter) SendText(ctx context.Context, to, text, replyTo string, mentions []string) (string, int64, error) {
	toJID, err := parseJID(to)
	if err != nil {
		return "", 0, err
	}

	// A plain Conversation suffices when there is no reply context and no
	// mentions; otherwise use ExtendedTextMessage so we can attach ContextInfo.
	if replyTo == "" && len(mentions) == 0 {
		return a.send(ctx, toJID, &waE2E.Message{Conversation: proto.String(text)})
	}

	ctxInfo := &waE2E.ContextInfo{}
	if replyTo != "" {
		ctxInfo.StanzaID = proto.String(replyTo)
		// Participant is the quoted sender; for a self-reply we leave it unset.
	}
	if len(mentions) > 0 {
		ctxInfo.MentionedJID = mentions
	}
	return a.send(ctx, toJID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: ctxInfo,
		},
	})
}

func (a *whatsmeowAdapter) SendPoll(ctx context.Context, to, name string, options []string, selectableCount int) (string, int64, error) {
	toJID, err := parseJID(to)
	if err != nil {
		return "", 0, err
	}
	msg := a.cli.BuildPollCreation(name, options, selectableCount)
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
	// richer forward (re-uploading media, copying the original body) belongs to
	// Phase 3 once a fetch-message-by-id path exists — see outbound-pipeline.md.
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
