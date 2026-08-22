// Package enginegrpc exposes the narrow private gateway engine RPC surface.
package enginegrpc

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	gatewayv1.UnimplementedGatewayEngineServiceServer
	GatewayID string
	Engine    application.GatewayEngine
}

func (s *Server) GetSessionState(ctx context.Context, req *gatewayv1.GetSessionStateRequest) (*gatewayv1.GetSessionStateResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid session-state request")
	}
	value, err := s.Engine.GetSessionState(ctx, application.SessionStateQuery{OrganizationID: target.OrganizationId, SessionID: target.SessionId, GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch()})
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.GetSessionStateResponse{Target: target, Status: sessionStatus(value.Status), Connected: value.Connected, LoggedIn: value.LoggedIn}, nil
}

func (s *Server) SetAccountPresence(ctx context.Context, req *gatewayv1.SetAccountPresenceRequest) (*gatewayv1.SetAccountPresenceResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 || req.GetCommandId() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid mutation request")
	}
	state := application.AccountPresence("")
	switch req.GetState() {
	case gatewayv1.AccountPresence_ACCOUNT_PRESENCE_ONLINE:
		state = application.AccountPresenceOnline
	case gatewayv1.AccountPresence_ACCOUNT_PRESENCE_OFFLINE:
		state = application.AccountPresenceOffline
	default:
		return nil, status.Error(codes.InvalidArgument, "invalid presence state")
	}
	result, err := s.Engine.SetAccountPresence(ctx, application.SetPresenceCommand{CommandID: req.GetCommandId(), OrganizationID: target.OrganizationId, SessionID: target.SessionId, GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(), State: state})
	if err != nil {
		return nil, grpcError(err)
	}
	return presenceResponse(result), nil
}

func (s *Server) MarkRead(ctx context.Context, req *gatewayv1.MarkReadRequest) (*gatewayv1.MarkReadResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 || req.GetCommandId() == "" || len(req.GetMessageIds()) == 0 || req.GetReadAtUnixMs() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid mark-read request")
	}
	result, err := s.Engine.MarkRead(ctx, application.MarkReadCommand{CommandID: req.GetCommandId(), OrganizationID: target.OrganizationId, SessionID: target.SessionId, GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(), ChatJID: req.GetChatJid(), SenderJID: req.GetSenderJid(), MessageIDs: req.GetMessageIds(), ReadAt: time.UnixMilli(req.GetReadAtUnixMs()).UTC()})
	if err != nil {
		return nil, grpcError(err)
	}
	return readResponse(result), nil
}

func (s *Server) SendMessage(ctx context.Context, req *gatewayv1.SendMessageRequest) (*gatewayv1.SendMessageResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 || req.GetCommandId() == "" || len(req.GetPayloadJson()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid send-message request")
	}
	var payload domain.SendRequest
	if err := json.Unmarshal(req.GetPayloadJson(), &payload); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid send payload")
	}
	result, err := s.Engine.SendMessage(ctx, application.SendCommand{
		CommandID: req.GetCommandId(), OrganizationID: target.OrganizationId, SessionID: target.SessionId,
		GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(), Payload: payload,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.SendMessageResponse{
		CommandId: result.CommandID, Target: targetResponse(result.MutationResult), AssignmentEpoch: result.AssignmentEpoch,
		WaMessageId: result.WAMessageID, SentAtUnixMs: result.SentAt.UnixMilli(),
	}, nil
}

func (s *Server) MessageOp(ctx context.Context, req *gatewayv1.MessageOpRequest) (*gatewayv1.MessageOpResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 || req.GetCommandId() == "" || req.GetOp() == "" || req.GetMessageId() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid message-op request")
	}
	result, err := s.Engine.ExecuteOp(ctx, application.MessageOpCommand{
		CommandID: req.GetCommandId(), OrganizationID: target.OrganizationId, SessionID: target.SessionId,
		GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(),
		Op: application.MessageOp(req.GetOp()), ChatJID: req.GetChatJid(), SenderJID: req.GetSenderJid(),
		MessageID: req.GetMessageId(), Emoji: req.GetEmoji(), NewText: req.GetNewText(),
		Options: req.GetOptions(), ToJID: req.GetToJid(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.MessageOpResponse{
		CommandId: result.CommandID, Target: targetResponse(result.MutationResult), AssignmentEpoch: result.AssignmentEpoch,
	}, nil
}

func (s *Server) target(target *gatewayv1.SessionTarget) (*gatewayv1.SessionTarget, error) {
	if s.Engine == nil || s.GatewayID == "" || target == nil || target.GetOrganizationId() == "" || target.GetSessionId() == "" || target.GetGatewayId() != s.GatewayID {
		return nil, domain.ErrValidation("invalid gateway target")
	}
	return target, nil
}

func presenceResponse(value application.MutationResult) *gatewayv1.SetAccountPresenceResponse {
	return &gatewayv1.SetAccountPresenceResponse{CommandId: value.CommandID, Target: targetResponse(value), AssignmentEpoch: value.AssignmentEpoch}
}
func readResponse(value application.MutationResult) *gatewayv1.MarkReadResponse {
	return &gatewayv1.MarkReadResponse{CommandId: value.CommandID, Target: targetResponse(value), AssignmentEpoch: value.AssignmentEpoch}
}
func targetResponse(value application.MutationResult) *gatewayv1.SessionTarget {
	return &gatewayv1.SessionTarget{OrganizationId: value.OrganizationID, SessionId: value.SessionID, GatewayId: value.GatewayID}
}

func sessionStatus(value domain.SessionStatus) gatewayv1.GatewaySessionStatus {
	switch value {
	case domain.SessionStopped:
		return gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_STOPPED
	case domain.SessionStarting:
		return gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_STARTING
	case domain.SessionScanQR:
		return gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_SCAN_QR
	case domain.SessionWorking:
		return gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_WORKING
	case domain.SessionLoggedOut:
		return gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_LOGGED_OUT
	default:
		return gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_UNSPECIFIED
	}
}

func grpcError(err error) error {
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	}
	var apiErr *domain.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case domain.CodeValidationError:
			return status.Error(codes.InvalidArgument, "invalid request")
		case domain.CodeNotFound:
			return status.Error(codes.NotFound, "not found")
		case domain.CodeConflict:
			return status.Error(codes.FailedPrecondition, "assignment precondition failed")
		case domain.CodeForbidden:
			return status.Error(codes.PermissionDenied, "permission denied")
		}
	}
	return status.Error(codes.Internal, "gateway engine unavailable")
}
