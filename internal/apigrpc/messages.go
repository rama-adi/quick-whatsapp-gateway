package apigrpc

import (
	"context"

	publicv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/public/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// MessagesDeps is the outbound send + message-op + chat-history surface
// apigrpc consumes — the method sets of handlers.MessageSvc and the
// handlers.ChatSvc ListMessages route. The concrete *service.MessageService /
// *service.ChatService satisfy them.
type MessagesDeps interface {
	Send(ctx context.Context, organizationID, sessionID string, req domain.SendRequest, opts outbound.SendOptions) (outbound.SendResult, error)
	Edit(ctx context.Context, organizationID, sessionID, chat, msgID, newText string) (outbound.SendResult, error)
	Revoke(ctx context.Context, organizationID, sessionID, chat, sender, msgID string) (outbound.SendResult, error)
	React(ctx context.Context, organizationID, sessionID, chat, sender, msgID, emoji string) (outbound.SendResult, error)
	Forward(ctx context.Context, organizationID, sessionID, chat, sender, msgID, to string) (outbound.SendResult, error)
	Vote(ctx context.Context, organizationID, sessionID, chat, sender, msgID string, options []string) (outbound.SendResult, error)
}

var _ MessagesDeps = (*service.MessageService)(nil)

// ChatsReader is the chat-history read slice apigrpc consumes.
type ChatsReader interface {
	ListMessages(ctx context.Context, organizationID, sessionID, chatJID, cursor string, limit int) (store.Page[domain.Message], error)
}

var _ ChatsReader = (*service.ChatService)(nil)

// Messages implements publicv1.PublicMessagesService. Sends and message
// operations gate on `send` (matching the REST routes); the history read gates
// on `read`.
type Messages struct {
	publicv1.UnimplementedPublicMessagesServiceServer
	Messages MessagesDeps
	Chats    ChatsReader
}

// NewMessages builds the messages adapter.
func NewMessages(messages MessagesDeps, chats ChatsReader) *Messages {
	return &Messages{Messages: messages, Chats: chats}
}

func (m *Messages) SendMessage(ctx context.Context, req *publicv1.SendMessageRequest) (*publicv1.SendMessageResponse, error) {
	org, err := requireOrg(ctx, authz.CapSend)
	if err != nil {
		return nil, err
	}
	var body domain.SendRequest
	var opts outbound.SendOptions
	if req != nil {
		body = sendBodyFromProto(req.GetBody())
		opts = outbound.SendOptions{Async: req.GetAsync(), IdempotencyKey: req.GetIdempotencyKey()}
	}
	res, err := m.Messages.Send(ctx, org, req.GetSessionId(), body, opts)
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.SendMessageResponse{Result: sendResultToProto(res)}, nil
}

func (m *Messages) EditMessage(ctx context.Context, req *publicv1.EditMessageRequest) (*publicv1.EditMessageResponse, error) {
	org, err := requireOrg(ctx, authz.CapSend)
	if err != nil {
		return nil, err
	}
	res, err := m.Messages.Edit(ctx, org, req.GetSessionId(), req.GetChat(), req.GetMessageId(), req.GetText())
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.EditMessageResponse{Result: sendResultToProto(res)}, nil
}

func (m *Messages) RevokeMessage(ctx context.Context, req *publicv1.RevokeMessageRequest) (*publicv1.RevokeMessageResponse, error) {
	org, err := requireOrg(ctx, authz.CapSend)
	if err != nil {
		return nil, err
	}
	res, err := m.Messages.Revoke(ctx, org, req.GetSessionId(), req.GetChat(), req.GetSender(), req.GetMessageId())
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.RevokeMessageResponse{Result: sendResultToProto(res)}, nil
}

func (m *Messages) AddReaction(ctx context.Context, req *publicv1.AddReactionRequest) (*publicv1.AddReactionResponse, error) {
	org, err := requireOrg(ctx, authz.CapSend)
	if err != nil {
		return nil, err
	}
	res, err := m.Messages.React(ctx, org, req.GetSessionId(), req.GetChat(), req.GetSender(), req.GetMessageId(), req.GetEmoji())
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.AddReactionResponse{Result: sendResultToProto(res)}, nil
}

func (m *Messages) RemoveReaction(ctx context.Context, req *publicv1.RemoveReactionRequest) (*publicv1.RemoveReactionResponse, error) {
	org, err := requireOrg(ctx, authz.CapSend)
	if err != nil {
		return nil, err
	}
	// Empty emoji clears the reaction — same as the REST DELETE route.
	res, err := m.Messages.React(ctx, org, req.GetSessionId(), req.GetChat(), req.GetSender(), req.GetMessageId(), "")
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.RemoveReactionResponse{Result: sendResultToProto(res)}, nil
}

func (m *Messages) ForwardMessage(ctx context.Context, req *publicv1.ForwardMessageRequest) (*publicv1.ForwardMessageResponse, error) {
	org, err := requireOrg(ctx, authz.CapSend)
	if err != nil {
		return nil, err
	}
	res, err := m.Messages.Forward(ctx, org, req.GetSessionId(), req.GetChat(), req.GetSender(), req.GetMessageId(), req.GetTo())
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.ForwardMessageResponse{Result: sendResultToProto(res)}, nil
}

func (m *Messages) VotePoll(ctx context.Context, req *publicv1.VotePollRequest) (*publicv1.VotePollResponse, error) {
	org, err := requireOrg(ctx, authz.CapSend)
	if err != nil {
		return nil, err
	}
	res, err := m.Messages.Vote(ctx, org, req.GetSessionId(), req.GetChat(), req.GetSender(), req.GetMessageId(), req.GetOptions())
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.VotePollResponse{Result: sendResultToProto(res)}, nil
}

func (m *Messages) ListMessages(ctx context.Context, req *publicv1.ListMessagesRequest) (*publicv1.ListMessagesResponse, error) {
	org, err := requireOrg(ctx, authz.CapRead)
	if err != nil {
		return nil, err
	}
	page, err := m.Chats.ListMessages(ctx, org, req.GetSessionId(), req.GetChatJid(), req.GetCursor(), clampLimit(int(req.GetLimit())))
	if err != nil {
		return nil, Status(err)
	}
	out := &publicv1.ListMessagesResponse{
		Messages:   make([]*publicv1.StoredMessage, 0, len(page.Items)),
		NextCursor: page.NextCursor,
	}
	for i := range page.Items {
		out.Messages = append(out.Messages, messageToProto(page.Items[i]))
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// translation helpers
// ---------------------------------------------------------------------------

// sendBodyFromProto translates the flat discriminated send body; field names
// mirror the REST JSON contract one-for-one.
func sendBodyFromProto(b *publicv1.SendMessageBody) domain.SendRequest {
	if b == nil {
		return domain.SendRequest{}
	}
	req := domain.SendRequest{
		Type:            b.GetType(),
		To:              b.GetTo(),
		Text:            b.GetText(),
		ReplyTo:         b.GetReplyTo(),
		Mentions:        b.GetMentions(),
		Name:            b.GetName(),
		Options:         b.GetOptions(),
		SelectableCount: int(b.GetSelectableCount()),
		PollEndTime:     b.GetPollEndTimeUnixMs(),
		PollHideVotes:   b.GetPollHideVotes(),
		Latitude:        b.GetLatitude(),
		Longitude:       b.GetLongitude(),
		Caption:         b.GetCaption(),
	}
	if b.GetContact() != nil {
		req.Contact = &domain.ContactCard{
			Name:  b.Contact.GetName(),
			Phone: b.Contact.GetPhone(),
			VCard: b.Contact.GetVcard(),
		}
	}
	if b.GetMedia() != nil {
		req.Media = &domain.MediaPayload{
			Data:     b.Media.GetData(),
			URL:      b.Media.GetUrl(),
			Mimetype: b.Media.GetMimetype(),
			Caption:  b.Media.GetCaption(),
			Filename: b.Media.GetFilename(),
		}
	}
	for _, item := range b.GetMedias() {
		req.Medias = append(req.Medias, domain.AlbumMediaPayload{
			Type:     item.GetType(),
			Data:     item.GetData(),
			URL:      item.GetUrl(),
			Mimetype: item.GetMimetype(),
		})
	}
	return req
}

func messageStatusToProto(s domain.MessageStatus) publicv1.MessageStatus {
	switch s {
	case domain.MessagePending:
		return publicv1.MessageStatus_MESSAGE_STATUS_PENDING
	case domain.MessageSent:
		return publicv1.MessageStatus_MESSAGE_STATUS_SENT
	case domain.MessageDelivered:
		return publicv1.MessageStatus_MESSAGE_STATUS_DELIVERED
	case domain.MessageRead:
		return publicv1.MessageStatus_MESSAGE_STATUS_READ
	case domain.MessagePlayed:
		return publicv1.MessageStatus_MESSAGE_STATUS_PLAYED
	case domain.MessageFailed:
		return publicv1.MessageStatus_MESSAGE_STATUS_FAILED
	default:
		return publicv1.MessageStatus_MESSAGE_STATUS_UNSPECIFIED
	}
}

func sendResultToProto(r outbound.SendResult) *publicv1.SendResult {
	var statusField publicv1.MessageStatus
	if r.Status != "" {
		statusField = messageStatusToProto(r.Status)
	}
	var ts *int64
	if r.Timestamp != 0 {
		v := r.Timestamp
		ts = &v
	}
	var outbox *string
	if r.OutboxID != "" {
		v := r.OutboxID
		outbox = &v
	}
	var waMsg *string
	if r.WAMessageID != "" {
		v := r.WAMessageID
		waMsg = &v
	}
	return &publicv1.SendResult{
		Mode:           r.Mode,
		WaMessageId:    waMsg,
		Status:         statusField,
		TimestampUnixMs: ts,
		OutboxId:       outbox,
		Replayed:       r.Replayed,
	}
}

func directionToProto(d domain.MessageDirection) publicv1.MessageDirection {
	if d == domain.DirectionOut {
		return publicv1.MessageDirection_MESSAGE_DIRECTION_OUT
	}
	if d == domain.DirectionIn {
		return publicv1.MessageDirection_MESSAGE_DIRECTION_IN
	}
	return publicv1.MessageDirection_MESSAGE_DIRECTION_UNSPECIFIED
}

func chatTypeToProto(t domain.ChatType) publicv1.ChatType {
	switch t {
	case domain.ChatDM:
		return publicv1.ChatType_CHAT_TYPE_DM
	case domain.ChatGroup:
		return publicv1.ChatType_CHAT_TYPE_GROUP
	case domain.ChatNewsletter:
		return publicv1.ChatType_CHAT_TYPE_NEWSLETTER
	case domain.ChatBroadcast:
		return publicv1.ChatType_CHAT_TYPE_BROADCAST
	case domain.ChatStatus:
		return publicv1.ChatType_CHAT_TYPE_STATUS
	default:
		return publicv1.ChatType_CHAT_TYPE_UNSPECIFIED
	}
}

func strPtrToProto(v *string) *string {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func i64PtrToProto(v *int64) *int64 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func messageToProto(m domain.Message) *publicv1.StoredMessage {
	msg := &publicv1.StoredMessage{
		Id:               m.ID,
		SessionId:        m.SessionID,
		WaMessageId:      m.WAMessageID,
		ChatJid:          m.ChatJID,
		SenderLid:        strPtrToProto(m.SenderLID),
		SenderJid:        strPtrToProto(m.SenderJID),
		SenderName:       strPtrToProto(m.SenderName),
		FromMe:           m.FromMe,
		Direction:        directionToProto(m.Direction),
		Type:             m.Type,
		Body:             strPtrToProto(m.Body),
		QuotedMessageId:  strPtrToProto(m.QuotedMessageID),
		HasMedia:         m.HasMedia,
		AckLevel:         i32PtrToProto(m.AckLevel),
		Error:            strPtrToProto(m.Error),
		Edited:           m.Edited,
		Deleted:          m.Deleted,
		TimestampUnixMs:  m.Timestamp,
		CreatedAtUnixMs:  m.CreatedAt,
		MentionNames:     m.MentionNames,
	}
	for _, mention := range m.Mentions {
		msg.Mentions = append(msg.Mentions, string(mention))
	}
	if len(m.MentionNames) == 0 {
		msg.MentionNames = nil
	}
	if m.Status != nil && *m.Status != "" {
		s := messageStatusToProto(*m.Status)
		msg.Status = &s
	}
	if m.MediaMeta != nil {
		msg.Media = &publicv1.MediaMeta{
			Mimetype: m.MediaMeta.Mimetype,
			Size:     m.MediaMeta.Size,
			Filename: m.MediaMeta.Filename,
		}
	}
	return msg
}

func i32PtrToProto(v *int) *int32 {
	if v == nil {
		return nil
	}
	c := int32(*v)
	return &c
}
