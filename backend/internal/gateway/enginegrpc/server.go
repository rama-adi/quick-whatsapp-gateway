// Package enginegrpc exposes the narrow private gateway engine RPC surface.
package enginegrpc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	gatewayv1.UnimplementedGatewayEngineServiceServer
	GatewayID string
	Engine    application.GatewayEngine
}

func (s *Server) GetSessionState(
	ctx context.Context,
	req *gatewayv1.GetSessionStateRequest,
) (*gatewayv1.GetSessionStateResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid session-state request")
	}
	value, err := s.Engine.GetSessionState(ctx, sessionQuery(target, req.GetAssignmentEpoch()))
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.GetSessionStateResponse{
		Target:    target,
		Status:    sessionStatus(value.Status),
		Connected: value.Connected,
		LoggedIn:  value.LoggedIn,
	}, nil
}

func (s *Server) SetAccountPresence(
	ctx context.Context,
	req *gatewayv1.SetAccountPresenceRequest,
) (*gatewayv1.SetAccountPresenceResponse, error) {
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
	result, err := s.Engine.SetAccountPresence(ctx, application.SetPresenceCommand{
		CommandID:       req.GetCommandId(),
		OrganizationID:  target.OrganizationId,
		SessionID:       target.SessionId,
		GatewayID:       target.GatewayId,
		AssignmentEpoch: req.GetAssignmentEpoch(),
		State:           state,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return presenceResponse(result), nil
}

func (s *Server) MarkRead(
	ctx context.Context,
	req *gatewayv1.MarkReadRequest,
) (*gatewayv1.MarkReadResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	missingMessageIDs := len(req.GetMessageIds()) == 0
	invalidTimestamp := req.GetReadAtUnixMs() <= 0
	if req.GetAssignmentEpoch() == 0 || req.GetCommandId() == "" || missingMessageIDs || invalidTimestamp {
		return nil, status.Error(codes.InvalidArgument, "invalid mark-read request")
	}
	result, err := s.Engine.MarkRead(ctx, application.MarkReadCommand{
		CommandID:       req.GetCommandId(),
		OrganizationID:  target.OrganizationId,
		SessionID:       target.SessionId,
		GatewayID:       target.GatewayId,
		AssignmentEpoch: req.GetAssignmentEpoch(),
		ChatJID:         req.GetChatJid(),
		SenderJID:       req.GetSenderJid(),
		MessageIDs:      req.GetMessageIds(),
		ReadAt:          time.UnixMilli(req.GetReadAtUnixMs()).UTC(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return readResponse(result), nil
}

func (s *Server) SendMessage(
	ctx context.Context,
	req *gatewayv1.SendMessageRequest,
) (*gatewayv1.SendMessageResponse, error) {
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
	if quote := req.GetQuoteContext(); quote != nil && payload.ReplyTo != "" {
		payload.QuoteContext = &domain.SendQuoteContext{
			ChatJID:   quote.GetChatJid(),
			SenderJID: quote.GetSenderJid(),
			Type:      quote.GetType(),
			Body:      quote.GetBody(),
			FromMe:    quote.GetFromMe(),
		}
	}
	result, err := s.Engine.SendMessage(ctx, application.SendCommand{
		CommandID: req.GetCommandId(), OrganizationID: target.OrganizationId, SessionID: target.SessionId,
		GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(), Payload: payload,
	})
	if err != nil {
		slog.Error("gateway send failed", "session", target.SessionId, "type", payload.Type, "err", err)
		return nil, grpcError(err)
	}
	return &gatewayv1.SendMessageResponse{
		CommandId:       result.CommandID,
		Target:          targetResponse(result.MutationResult),
		AssignmentEpoch: result.AssignmentEpoch,
		WaMessageId:     result.WAMessageID,
		SentAtUnixMs:    result.SentAt.UnixMilli(),
	}, nil
}

func (s *Server) MessageOp(
	ctx context.Context,
	req *gatewayv1.MessageOpRequest,
) (*gatewayv1.MessageOpResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	missingCommand := req.GetAssignmentEpoch() == 0 || req.GetCommandId() == ""
	if missingCommand || req.GetOp() == "" || req.GetMessageId() == "" {
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
		WaMessageId: result.WAMessageID, SentAtUnixMs: result.SentAt.UnixMilli(),
	}, nil
}

func (s *Server) LookupContact(
	ctx context.Context,
	req *gatewayv1.LookupContactRequest,
) (*gatewayv1.LookupContactResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 || len(req.GetPhones()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid contact-lookup request")
	}
	results, err := s.Engine.LookupContact(ctx, application.LookupContactCommand{
		OrganizationID: target.OrganizationId, SessionID: target.SessionId,
		GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(), Phones: req.GetPhones(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	response := &gatewayv1.LookupContactResponse{Results: make([]*gatewayv1.ContactLookup, 0, len(results))}
	for _, r := range results {
		response.Results = append(response.Results, &gatewayv1.ContactLookup{
			Query:        r.Query,
			Jid:          r.JID,
			IsOnWhatsapp: r.IsIn,
		})
	}
	return response, nil
}

func (s *Server) GetContactPicture(
	ctx context.Context,
	req *gatewayv1.GetContactPictureRequest,
) (*gatewayv1.GetContactPictureResponse, error) {
	query, err := s.queryTarget(req.GetTarget(), req.GetAssignmentEpoch())
	if err != nil {
		return nil, grpcError(err)
	}
	picture, err := s.Engine.GetContactPicture(ctx, query, req.GetJid())
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.GetContactPictureResponse{Url: picture.URL, Id: picture.ID}, nil
}

func (s *Server) GetContactAbout(
	ctx context.Context,
	req *gatewayv1.GetContactAboutRequest,
) (*gatewayv1.GetContactAboutResponse, error) {
	query, err := s.queryTarget(req.GetTarget(), req.GetAssignmentEpoch())
	if err != nil {
		return nil, grpcError(err)
	}
	about, err := s.Engine.GetContactAbout(ctx, query, req.GetJid())
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.GetContactAboutResponse{About: about}, nil
}

func (s *Server) SetBlocked(
	ctx context.Context,
	req *gatewayv1.SetBlockedRequest,
) (*gatewayv1.SetBlockedResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 || req.GetCommandId() == "" || req.GetJid() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid set-blocked request")
	}
	result, err := s.Engine.SetBlocked(ctx, application.ContactJIDCommand{
		CommandID: req.GetCommandId(), OrganizationID: target.OrganizationId, SessionID: target.SessionId,
		GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(), JID: req.GetJid(), Blocked: req.GetBlocked(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.SetBlockedResponse{
		CommandId:       result.CommandID,
		Target:          targetResponse(result.MutationResult),
		AssignmentEpoch: result.AssignmentEpoch,
	}, nil
}

func (s *Server) CreateGroup(
	ctx context.Context,
	req *gatewayv1.CreateGroupRequest,
) (*gatewayv1.CreateGroupResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	missingCommand := req.GetAssignmentEpoch() == 0 || req.GetCommandId() == ""
	if missingCommand || req.GetName() == "" || len(req.GetParticipants()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid create-group request")
	}
	result, err := s.Engine.MutateGroup(ctx, application.GroupMutationCommand{
		CommandID: req.GetCommandId(), OrganizationID: target.OrganizationId, SessionID: target.SessionId,
		GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(),
		Kind: application.GroupOpCreate, Name: req.GetName(), Participants: req.GetParticipants(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	group := result.CreatedGroup
	return &gatewayv1.CreateGroupResponse{
		CommandId: result.CommandID, Target: targetResponse(result.MutationResult), AssignmentEpoch: result.AssignmentEpoch,
		Group: groupInfoProto(group),
	}, nil
}

func (s *Server) UpdateGroupSettings(
	ctx context.Context,
	req *gatewayv1.UpdateGroupSettingsRequest,
) (*gatewayv1.UpdateGroupSettingsResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	settings := application.GroupSettingsUpdate{
		Subject:     req.Subject,
		Description: req.Description,
		Announce:    req.Announce,
		Locked:      req.Locked,
	}
	noSettingGiven := settings.Subject == nil &&
		settings.Description == nil &&
		settings.Announce == nil &&
		settings.Locked == nil
	missingCommand := req.GetAssignmentEpoch() == 0 || req.GetCommandId() == "" || req.GetGroupJid() == ""
	if missingCommand || noSettingGiven {
		return nil, status.Error(codes.InvalidArgument, "invalid update-group-settings request")
	}
	result, err := s.Engine.MutateGroup(ctx, application.GroupMutationCommand{
		CommandID: req.GetCommandId(), OrganizationID: target.OrganizationId, SessionID: target.SessionId,
		GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(),
		Kind: application.GroupOpUpdateSettings, GroupJID: req.GetGroupJid(), Settings: settings,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.UpdateGroupSettingsResponse{
		CommandId:       result.CommandID,
		Target:          targetResponse(result.MutationResult),
		AssignmentEpoch: result.AssignmentEpoch,
	}, nil
}

func (s *Server) UpdateGroupParticipants(
	ctx context.Context,
	req *gatewayv1.UpdateGroupParticipantsRequest,
) (*gatewayv1.UpdateGroupParticipantsResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	action, validAction := participantChange(req.GetAction())
	missingCommand := req.GetAssignmentEpoch() == 0 || req.GetCommandId() == "" || req.GetGroupJid() == ""
	if missingCommand || len(req.GetParticipants()) == 0 || !validAction {
		return nil, status.Error(codes.InvalidArgument, "invalid update-group-participants request")
	}
	result, err := s.Engine.MutateGroup(ctx, application.GroupMutationCommand{
		CommandID: req.GetCommandId(), OrganizationID: target.OrganizationId, SessionID: target.SessionId,
		GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(),
		Kind: application.GroupOpUpdateParticipants, GroupJID: req.GetGroupJid(),
		Participants: req.GetParticipants(), Action: action,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.UpdateGroupParticipantsResponse{
		CommandId:       result.CommandID,
		Target:          targetResponse(result.MutationResult),
		AssignmentEpoch: result.AssignmentEpoch,
	}, nil
}

func (s *Server) GetGroupInviteLink(
	ctx context.Context,
	req *gatewayv1.GetGroupInviteLinkRequest,
) (*gatewayv1.GetGroupInviteLinkResponse, error) {
	query, err := s.queryTarget(req.GetTarget(), req.GetAssignmentEpoch())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetGroupJid() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid invite-link request")
	}
	link, err := s.Engine.GetGroupInviteLink(ctx, query, req.GetGroupJid(), req.GetReset_())
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.GetGroupInviteLinkResponse{Link: link}, nil
}

func (s *Server) JoinGroup(
	ctx context.Context,
	req *gatewayv1.JoinGroupRequest,
) (*gatewayv1.JoinGroupResponse, error) {
	query, err := s.queryTarget(req.GetTarget(), req.GetAssignmentEpoch())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetInvite() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid join-group request")
	}
	groupJID, err := s.Engine.JoinGroup(ctx, query, req.GetInvite())
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.JoinGroupResponse{GroupJid: groupJID}, nil
}

func (s *Server) LeaveGroup(
	ctx context.Context,
	req *gatewayv1.LeaveGroupRequest,
) (*gatewayv1.LeaveGroupResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 || req.GetCommandId() == "" || req.GetGroupJid() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid leave-group request")
	}
	result, err := s.Engine.MutateGroup(ctx, application.GroupMutationCommand{
		CommandID: req.GetCommandId(), OrganizationID: target.OrganizationId, SessionID: target.SessionId,
		GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(),
		Kind: application.GroupOpLeave, GroupJID: req.GetGroupJid(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.LeaveGroupResponse{
		CommandId:       result.CommandID,
		Target:          targetResponse(result.MutationResult),
		AssignmentEpoch: result.AssignmentEpoch,
	}, nil
}

func (s *Server) GetChatPresence(
	ctx context.Context,
	req *gatewayv1.GetChatPresenceRequest,
) (*gatewayv1.GetChatPresenceResponse, error) {
	query, err := s.queryTarget(req.GetTarget(), req.GetAssignmentEpoch())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetChatJid() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid chat-presence request")
	}
	presence, err := s.Engine.GetChatPresence(ctx, query, req.GetChatJid())
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.GetChatPresenceResponse{Presence: presenceStatusProto(presence)}, nil
}

func (s *Server) SetChatPresence(
	ctx context.Context,
	req *gatewayv1.SetChatPresenceRequest,
) (*gatewayv1.SetChatPresenceResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 || req.GetChatJid() == "" || req.GetState() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid chat-presence mutation request")
	}
	err = s.Engine.SetChatPresence(ctx, application.ChatPresenceCommand{
		OrganizationID: target.OrganizationId, SessionID: target.SessionId,
		GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(),
		ChatJID: req.GetChatJid(), State: req.GetState(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.SetChatPresenceResponse{}, nil
}

func (s *Server) BackfillSession(
	ctx context.Context,
	req *gatewayv1.BackfillSessionRequest,
) (*gatewayv1.BackfillSessionResponse, error) {
	query, err := s.queryTarget(req.GetTarget(), req.GetAssignmentEpoch())
	if err != nil {
		return nil, grpcError(err)
	}
	snapshot, err := s.Engine.BackfillSession(ctx, query)
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.BackfillSessionResponse{Snapshot: backfillSnapshotProto(snapshot)}, nil
}

func (s *Server) PrepareSession(
	ctx context.Context,
	req *gatewayv1.PrepareSessionRequest,
) (*gatewayv1.PrepareSessionResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid prepare-session request")
	}
	result, err := s.Engine.PrepareSession(ctx, sessionQuery(target, req.GetAssignmentEpoch()))
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.PrepareSessionResponse{Target: target, AssignmentEpoch: result.AssignmentEpoch}, nil
}

func (s *Server) BeginPairing(
	ctx context.Context,
	req *gatewayv1.BeginPairingRequest,
) (*gatewayv1.BeginPairingResponse, error) {
	query, err := s.queryTarget(req.GetTarget(), req.GetAssignmentEpoch())
	if err != nil {
		return nil, grpcError(err)
	}
	snapshot, err := s.Engine.BeginPairing(ctx, query)
	if err != nil {
		return nil, grpcError(err)
	}
	response := &gatewayv1.BeginPairingResponse{Target: targetProto(query), AssignmentEpoch: query.AssignmentEpoch}
	if snapshot.Code != "" {
		response.QrCode, response.QrExpiresAtUnixMs = &snapshot.Code, &snapshot.ExpiresAt
	}
	return response, nil
}

func (s *Server) PairPhone(
	ctx context.Context,
	req *gatewayv1.PairPhoneRequest,
) (*gatewayv1.PairPhoneResponse, error) {
	query, err := s.queryTarget(req.GetTarget(), req.GetAssignmentEpoch())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetPhone() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid pair-phone request")
	}
	code, err := s.Engine.PairPhone(ctx, query, req.GetPhone())
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.PairPhoneResponse{
		Target:          targetProto(query),
		AssignmentEpoch: query.AssignmentEpoch,
		PairingCode:     code,
	}, nil
}

func (s *Server) LogoutSession(
	ctx context.Context,
	req *gatewayv1.LogoutSessionRequest,
) (*gatewayv1.LogoutSessionResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if req.GetAssignmentEpoch() == 0 || req.GetCommandId() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid logout-session request")
	}
	result, err := s.Engine.LogoutSession(ctx, application.ContactJIDCommand{
		CommandID: req.GetCommandId(), OrganizationID: target.OrganizationId, SessionID: target.SessionId,
		GatewayID: target.GatewayId, AssignmentEpoch: req.GetAssignmentEpoch(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.LogoutSessionResponse{
		CommandId:       result.CommandID,
		Target:          targetResponse(result.MutationResult),
		AssignmentEpoch: result.AssignmentEpoch,
	}, nil
}

func (s *Server) ForgetSession(
	ctx context.Context,
	req *gatewayv1.ForgetSessionRequest,
) (*gatewayv1.ForgetSessionResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.Engine.ForgetSession(ctx, target.OrganizationId, target.SessionId); err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.ForgetSessionResponse{Target: target}, nil
}

func (s *Server) target(target *gatewayv1.SessionTarget) (*gatewayv1.SessionTarget, error) {
	misconfigured := s.Engine == nil || s.GatewayID == ""
	incomplete := target == nil || target.GetOrganizationId() == "" || target.GetSessionId() == ""
	foreign := target != nil && target.GetGatewayId() != s.GatewayID
	if misconfigured || incomplete || foreign {
		return nil, domain.ErrValidation("invalid gateway target")
	}
	return target, nil
}

func presenceResponse(value application.MutationResult) *gatewayv1.SetAccountPresenceResponse {
	return &gatewayv1.SetAccountPresenceResponse{
		CommandId:       value.CommandID,
		Target:          targetResponse(value),
		AssignmentEpoch: value.AssignmentEpoch,
	}
}

func readResponse(value application.MutationResult) *gatewayv1.MarkReadResponse {
	return &gatewayv1.MarkReadResponse{
		CommandId:       value.CommandID,
		Target:          targetResponse(value),
		AssignmentEpoch: value.AssignmentEpoch,
	}
}

func targetResponse(value application.MutationResult) *gatewayv1.SessionTarget {
	return &gatewayv1.SessionTarget{
		OrganizationId: value.OrganizationID,
		SessionId:      value.SessionID,
		GatewayId:      value.GatewayID,
	}
}

// sessionQuery builds the routing metadata shared by session-state reads.
func sessionQuery(target *gatewayv1.SessionTarget, assignmentEpoch uint64) application.SessionStateQuery {
	return application.SessionStateQuery{
		OrganizationID:  target.OrganizationId,
		SessionID:       target.SessionId,
		GatewayID:       target.GatewayId,
		AssignmentEpoch: assignmentEpoch,
	}
}

func targetProto(query application.SessionStateQuery) *gatewayv1.SessionTarget {
	return &gatewayv1.SessionTarget{
		OrganizationId: query.OrganizationID,
		SessionId:      query.SessionID,
		GatewayId:      query.GatewayID,
	}
}

// queryTarget validates a read request's routing metadata without requiring a
// command id.
func (s *Server) queryTarget(
	target *gatewayv1.SessionTarget,
	assignmentEpoch uint64,
) (application.SessionStateQuery, error) {
	value, err := s.target(target)
	if err != nil {
		return application.SessionStateQuery{}, err
	}
	if assignmentEpoch == 0 {
		return application.SessionStateQuery{}, domain.ErrValidation("assignment_epoch must be at least 1")
	}
	return application.SessionStateQuery{
		OrganizationID: value.OrganizationId, SessionID: value.SessionId,
		GatewayID: value.GatewayId, AssignmentEpoch: assignmentEpoch,
	}, nil
}

func groupInfoProto(info application.GroupInfoResult) *gatewayv1.GroupInfo {
	return &gatewayv1.GroupInfo{
		GroupJid: info.GroupJID, Subject: info.Subject, Description: info.Description,
		OwnerJid: info.OwnerJID, Participants: info.Participants,
		IsAnnounce: info.IsAnnounce, IsLocked: info.IsLocked,
	}
}

func participantChange(action gatewayv1.GroupParticipantChange) (application.GroupParticipantChange, bool) {
	switch action {
	case gatewayv1.GroupParticipantChange_GROUP_PARTICIPANT_CHANGE_ADD:
		return application.GroupChangeAdd, true
	case gatewayv1.GroupParticipantChange_GROUP_PARTICIPANT_CHANGE_REMOVE:
		return application.GroupChangeRemove, true
	case gatewayv1.GroupParticipantChange_GROUP_PARTICIPANT_CHANGE_PROMOTE:
		return application.GroupChangePromote, true
	case gatewayv1.GroupParticipantChange_GROUP_PARTICIPANT_CHANGE_DEMOTE:
		return application.GroupChangeDemote, true
	default:
		return "", false
	}
}

func presenceStatusProto(status domain.PresenceStatus) *gatewayv1.ChatPresenceStatus {
	return &gatewayv1.ChatPresenceStatus{
		ChatJid: status.ChatJID, From: status.From, State: status.State, Media: status.Media,
		Unavailable: status.Unavailable, LastSeenUnixMs: status.LastSeen,
	}
}

func backfillSnapshotProto(snapshot domain.BackfillSnapshot) *gatewayv1.BackfillSnapshot {
	out := &gatewayv1.BackfillSnapshot{
		Contacts: make([]*gatewayv1.BackfillContact, 0, len(snapshot.Contacts)),
		Groups:   make([]*gatewayv1.BackfillGroup, 0, len(snapshot.Groups)),
	}
	for _, c := range snapshot.Contacts {
		out.Contacts = append(out.Contacts, &gatewayv1.BackfillContact{
			Lid: c.LID, PhoneJid: c.PhoneJID, PhoneNumber: c.PhoneNumber, Name: c.Name, BusinessName: c.BusinessName,
		})
	}
	for _, g := range snapshot.Groups {
		members := make([]*gatewayv1.BackfillMember, 0, len(g.Members))
		for _, m := range g.Members {
			members = append(members, &gatewayv1.BackfillMember{
				Lid: m.LID, Jid: m.JID, PhoneNumber: m.PhoneNumber, Tag: m.Tag, Name: m.Name, Role: string(m.Role),
			})
		}
		out.Groups = append(out.Groups, &gatewayv1.BackfillGroup{
			GroupJid: g.GroupJID, Subject: g.Subject, Description: g.Description, OwnerJid: g.OwnerJID,
			Participants: int32(g.Participants), IsAnnounce: g.IsAnnounce, IsLocked: g.IsLocked,
			CreatedAtWaUnixMs: g.CreatedAtWA, Members: members,
		})
	}
	return out
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
