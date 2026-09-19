package service

import (
	"context"
	"strings"
	"sync"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/oidp"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/inbound"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// OIDPEventConsumer runs the WhatsApp login command interceptor after the API
// has durably ingested a message event. The gateway no longer has Redis or the
// OAuth pending state, so this is the API-side replacement for the old inbound
// pipeline hook.
type OIDPEventConsumer struct {
	mu          sync.RWMutex
	interceptor *oidp.LoginInterceptor
}

// NewOIDPEventConsumer builds a committed-event OAuth login consumer.
func NewOIDPEventConsumer(interceptor *oidp.LoginInterceptor) *OIDPEventConsumer {
	return &OIDPEventConsumer{interceptor: interceptor}
}

// SetInterceptor completes wiring after the API has built its private engine
// scheduler. Until then this consumer is a harmless no-op.
func (c *OIDPEventConsumer) SetInterceptor(interceptor *oidp.LoginInterceptor) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.interceptor = interceptor
	c.mu.Unlock()
}

// ConsumeCommittedEvent handles only inbound message events. Claim commands
// are deliberately intercepted before they reach normal chat persistence by
// the API worker's ordering; rejected claims are handled too, so credentials
// are never exposed as ordinary chat messages.
func (c *OIDPEventConsumer) ConsumeCommittedEvent(ctx context.Context, event domain.Event) error {
	_, err := c.HandleCommittedEvent(ctx, event)
	return err
}

// HandleCommittedEvent reports whether the message belonged to the OAuth
// interceptor. A handled claim, including a rejected claim, must stop the
// normal projection/fan-out chain so login credentials are not stored or sent
// to subscribers as chat content.
func (c *OIDPEventConsumer) HandleCommittedEvent(ctx context.Context, event domain.Event) (bool, error) {
	if c == nil || event.Type != domain.EventMessage {
		return false, nil
	}
	c.mu.RLock()
	interceptor := c.interceptor
	c.mu.RUnlock()
	if interceptor == nil {
		return false, nil
	}
	var payload apitypes.MessagePayload
	if err := decodePayload(event, &payload); err != nil {
		return false, err
	}
	if payload.FromMe {
		return false, nil
	}
	nm := normalizedLoginMessage(event, payload)
	handled, err := interceptor.HandleLogin(ctx, nm)
	return handled, err
}

func normalizedLoginMessage(event domain.Event, payload apitypes.MessagePayload) *inbound.NormalizedMessage {
	isGroup := strings.HasSuffix(payload.ChatJID, "@g.us")
	isDM := strings.HasSuffix(payload.ChatJID, "@s.whatsapp.net")
	mentions := make([]string, 0, len(payload.Mentions))
	for jid := range payload.Mentions {
		mentions = append(mentions, jid)
	}
	return &inbound.NormalizedMessage{
		Kind:           inbound.KindMessage,
		SessionID:      event.Session,
		OrganizationID: event.Organization,
		ChatJID:        payload.ChatJID,
		IsDM:           isDM,
		IsGroup:        isGroup,
		SenderLID:      payload.SenderLID,
		SenderJID:      payload.SenderJID,
		SenderPhone:    phoneFromJID(payload.SenderJID),
		PushName:       payload.PushName,
		WAMessageID:    payload.WAMessageID,
		MsgType:        payload.Type,
		Body:           payload.Body,
		Mentions:       mentions,
		TimestampMs:    payload.Timestamp,
	}
}

// OIDPBotFeedback adapts API-owned durable outbound commands to the feedback
// interface used by the login interceptor. Feedback remains best effort: the
// interceptor deliberately ignores these errors after a claim transition.
type OIDPBotFeedback struct {
	sender interface {
		Send(context.Context, string, string, domain.SendRequest, outbound.SendOptions) (outbound.SendResult, error)
		ExecuteOp(context.Context, string, string, outbound.OpRequest) (outbound.SendResult, error)
	}
}

// NewOIDPBotFeedback wires login feedback to the API outbound scheduler.
func NewOIDPBotFeedback(sender interface {
	Send(context.Context, string, string, domain.SendRequest, outbound.SendOptions) (outbound.SendResult, error)
	ExecuteOp(context.Context, string, string, outbound.OpRequest) (outbound.SendResult, error)
}) *OIDPBotFeedback {
	return &OIDPBotFeedback{sender: sender}
}

func (b *OIDPBotFeedback) React(
	ctx context.Context,
	organizationID, sessionID, chatJID, senderJID, messageID, emoji string,
) error {
	if b == nil || b.sender == nil {
		return nil
	}
	_, err := b.sender.ExecuteOp(ctx, organizationID, sessionID, outbound.OpRequest{
		Op: outbound.OpReaction, Chat: chatJID, Sender: senderJID, MsgID: messageID, Emoji: emoji,
	})
	return err
}

func (b *OIDPBotFeedback) Reply(
	ctx context.Context,
	organizationID, sessionID, chatJID, messageID, text string,
) error {
	if b == nil || b.sender == nil {
		return nil
	}
	_, err := b.sender.Send(ctx, organizationID, sessionID, domain.SendRequest{
		Type: domain.SendTypeText, To: chatJID, Text: text, ReplyTo: messageID,
	}, outbound.SendOptions{})
	return err
}

// OIDPGroupMemberChecker authorizes group-mode claims from API projections.
type OIDPGroupMemberChecker struct {
	members *store.GroupMemberRepo
}

// NewOIDPGroupMemberChecker wraps the projected group-membership repository.
func NewOIDPGroupMemberChecker(members *store.GroupMemberRepo) *OIDPGroupMemberChecker {
	return &OIDPGroupMemberChecker{members: members}
}

func (c *OIDPGroupMemberChecker) IsActiveGroupMember(
	ctx context.Context,
	sessionID, groupJID, senderLID string,
) (bool, error) {
	if c == nil || c.members == nil || senderLID == "" {
		return false, nil
	}
	members, err := c.members.ListByContact(ctx, sessionID, senderLID)
	if err != nil {
		return false, err
	}
	for _, member := range members {
		if member.GroupJID == groupJID {
			return true, nil
		}
	}
	return false, nil
}
