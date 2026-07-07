package service

import (
	"context"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

type OIDPBotFeedback struct {
	sessions *store.SessionRepo
	sender   *outbound.Sender
}

func NewOIDPBotFeedback(sessions *store.SessionRepo, sender *outbound.Sender) *OIDPBotFeedback {
	return &OIDPBotFeedback{sessions: sessions, sender: sender}
}

func (b *OIDPBotFeedback) React(ctx context.Context, organizationID, sessionID, chatJID, senderJID, messageID, emoji string) error {
	if b == nil || b.sender == nil {
		return nil
	}
	sess, err := b.sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	return discardSendResult(b.sender.SendOp(ctx, sess, outbound.OpRequest{
		Op: outbound.OpReaction, Chat: chatJID, Sender: senderJID, MsgID: messageID, Emoji: emoji,
	}))
}

func (b *OIDPBotFeedback) Reply(ctx context.Context, organizationID, sessionID, chatJID, messageID, text string) error {
	if b == nil || b.sender == nil {
		return nil
	}
	sess, err := b.sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	_, err = b.sender.Send(ctx, sess, domain.SendRequest{
		Type: domain.SendTypeText, To: chatJID, Text: text, ReplyTo: messageID,
	}, outbound.SendOptions{})
	return err
}

func discardSendResult(_ outbound.SendResult, err error) error { return err }

type OIDPGroupMemberChecker struct {
	members *store.GroupMemberRepo
}

func NewOIDPGroupMemberChecker(members *store.GroupMemberRepo) *OIDPGroupMemberChecker {
	return &OIDPGroupMemberChecker{members: members}
}

func (c *OIDPGroupMemberChecker) IsActiveGroupMember(ctx context.Context, sessionID, groupJID, senderLID string) (bool, error) {
	if c == nil || c.members == nil || senderLID == "" {
		return false, nil
	}
	members, err := c.members.ListByContact(ctx, sessionID, senderLID)
	if err != nil {
		return false, err
	}
	for _, m := range members {
		if m.GroupJID == groupJID {
			return true, nil
		}
	}
	return false, nil
}
