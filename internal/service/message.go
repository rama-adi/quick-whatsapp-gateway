package service

import (
	"context"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// MessageService runs the outbound send + message-operation pipeline (§8) on
// behalf of a organization's session. It resolves and organization-scopes the session, then
// delegates to the outbound.Sender (validation, idempotency, rate limiting,
// sync/async split all live there).
type MessageService struct {
	sessions *store.SessionRepo
	sender   *outbound.Sender
	log      *slog.Logger
	// gatewaySend is the control-plane send boundary (Increment 6). When set it
	// replaces the legacy in-process sender entirely: the legacy path remains
	// only for control-disabled deployments.
	gatewaySend GatewayMessageSender
	// gatewayOps is the control-plane message-operation boundary (Increment 7).
	gatewayOps GatewayMessageOpSender
}

// GatewayMessageSender is the API-owned outbound send boundary backed by the
// durable command scheduler and the private engine.
type GatewayMessageSender interface {
	Send(ctx context.Context, organizationID, sessionID string, req domain.SendRequest, opts outbound.SendOptions) (outbound.SendResult, error)
}

// NewMessageService constructs a MessageService.
func NewMessageService(sessions *store.SessionRepo, sender *outbound.Sender, log *slog.Logger) *MessageService {
	if log == nil {
		log = slog.Default()
	}
	return &MessageService{sessions: sessions, sender: sender, log: log}
}

// SetGatewaySendFacade routes every send through the API-owned scheduler.
func (s *MessageService) SetGatewaySendFacade(facade GatewayMessageSender) {
	if facade != nil {
		s.gatewaySend = facade
	}
}

// GatewayMessageOpSender is the API-owned message-operation boundary.
type GatewayMessageOpSender interface {
	ExecuteOp(ctx context.Context, organizationID, sessionID string, req outbound.OpRequest) (outbound.SendResult, error)
}

// SetGatewayOpFacade routes message sub-resource operations through the
// API-owned scheduler's durable command pipeline.
func (s *MessageService) SetGatewayOpFacade(facade GatewayMessageOpSender) {
	if facade != nil {
		s.gatewayOps = facade
	}
}

// session resolves a session and enforces organization ownership.
func (s *MessageService) session(ctx context.Context, organizationID, id string) (domain.WASession, error) {
	sess, err := s.sessions.Get(ctx, id)
	if err != nil {
		return domain.WASession{}, err
	}
	if sess.OrganizationID != organizationID {
		return domain.WASession{}, domain.ErrNotFound("session not found")
	}
	return sess, nil
}

// Send dispatches a unified typed send for a session.
func (s *MessageService) Send(ctx context.Context, organizationID, sessionID string, req domain.SendRequest, opts outbound.SendOptions) (outbound.SendResult, error) {
	if s.gatewaySend != nil {
		return s.gatewaySend.Send(ctx, organizationID, sessionID, req, opts)
	}
	sess, err := s.session(ctx, organizationID, sessionID)
	if err != nil {
		return outbound.SendResult{}, err
	}
	return s.sender.Send(ctx, sess, req, opts)
}

// op is the shared path for the message-operation sub-resources.
func (s *MessageService) op(ctx context.Context, organizationID, sessionID string, req outbound.OpRequest) (outbound.SendResult, error) {
	if s.gatewayOps != nil {
		return s.gatewayOps.ExecuteOp(ctx, organizationID, sessionID, req)
	}
	sess, err := s.session(ctx, organizationID, sessionID)
	if err != nil {
		return outbound.SendResult{}, err
	}
	return s.sender.SendOp(ctx, sess, req)
}

// Edit replaces the text of a previously sent message.
func (s *MessageService) Edit(ctx context.Context, organizationID, sessionID, chat, msgID, newText string) (outbound.SendResult, error) {
	return s.op(ctx, organizationID, sessionID, outbound.OpRequest{
		Op: outbound.OpEdit, Chat: chat, MsgID: msgID, NewText: newText,
	})
}

// Revoke deletes a message for everyone.
func (s *MessageService) Revoke(ctx context.Context, organizationID, sessionID, chat, sender, msgID string) (outbound.SendResult, error) {
	return s.op(ctx, organizationID, sessionID, outbound.OpRequest{
		Op: outbound.OpRevoke, Chat: chat, Sender: sender, MsgID: msgID,
	})
}

// React adds (emoji != "") or removes (emoji == "") a reaction.
func (s *MessageService) React(ctx context.Context, organizationID, sessionID, chat, sender, msgID, emoji string) (outbound.SendResult, error) {
	return s.op(ctx, organizationID, sessionID, outbound.OpRequest{
		Op: outbound.OpReaction, Chat: chat, Sender: sender, MsgID: msgID, Emoji: emoji,
	})
}

// Forward forwards a message to a destination chat.
func (s *MessageService) Forward(ctx context.Context, organizationID, sessionID, chat, sender, msgID, to string) (outbound.SendResult, error) {
	return s.op(ctx, organizationID, sessionID, outbound.OpRequest{
		Op: outbound.OpForward, Chat: chat, Sender: sender, MsgID: msgID, To: to,
	})
}

// Vote casts a poll vote on the given poll message.
func (s *MessageService) Vote(ctx context.Context, organizationID, sessionID, chat, sender, msgID string, options []string) (outbound.SendResult, error) {
	return s.op(ctx, organizationID, sessionID, outbound.OpRequest{
		Op: outbound.OpVote, Chat: chat, Sender: sender, MsgID: msgID, Options: options,
	})
}
