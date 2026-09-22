package apigrpc

import (
	"context"

	publicv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/public/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SessionsDeps is the session-lifecycle surface apigrpc consumes — the exact
// method set of handlers.SessionSvc used by the REST session routes. The
// concrete *service.SessionService satisfies it.
type SessionsDeps interface {
	Create(ctx context.Context, organizationID string, in service.CreateInput) (domain.WASession, error)
	List(ctx context.Context, organizationID string) ([]domain.WASession, error)
	Get(ctx context.Context, organizationID, id string) (domain.WASession, error)
	Start(ctx context.Context, organizationID, id string) error
	Stop(ctx context.Context, organizationID, id string) error
	Restart(ctx context.Context, organizationID, id string) error
	Logout(ctx context.Context, organizationID, id string) error
	Delete(ctx context.Context, organizationID, id string) error
	Me(ctx context.Context, organizationID, id string) (service.Me, error)
	QR(ctx context.Context, organizationID, id string) (service.QR, error)
	PairingCode(ctx context.Context, organizationID, id, phone string) (string, error)
}

var _ SessionsDeps = (*service.SessionService)(nil)

// Sessions implements publicv1.PublicSessionsService over SessionsDeps.
// Every RPC authorizes exactly like its REST route: a `manage` capability gate,
// then org scoping resolved from the principal (never a request field).
type Sessions struct {
	publicv1.UnimplementedPublicSessionsServiceServer
	Sessions SessionsDeps
}

// NewSessions builds the sessions adapter.
func NewSessions(sessions SessionsDeps) *Sessions { return &Sessions{Sessions: sessions} }

func (s *Sessions) CreateSession(
	ctx context.Context,
	req *publicv1.CreateSessionRequest,
) (*publicv1.CreateSessionResponse, error) {
	org, err := requireOrg(ctx, authz.CapManage)
	if err != nil {
		return nil, err
	}
	var in service.CreateInput
	if req != nil {
		in = service.CreateInput{
			Label:          req.Label,
			Start:          req.GetStart(),
			AutoRead:       req.AutoRead,
			PresenceTyping: req.PresenceTyping,
		}
	}
	sess, err := s.Sessions.Create(ctx, org, in)
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.CreateSessionResponse{Session: sessionToProto(sess)}, nil
}

func (s *Sessions) ListSessions(
	ctx context.Context,
	_ *publicv1.ListSessionsRequest,
) (*publicv1.ListSessionsResponse, error) {
	org, err := requireOrg(ctx, authz.CapManage)
	if err != nil {
		return nil, err
	}
	sessions, err := s.Sessions.List(ctx, org)
	if err != nil {
		return nil, Status(err)
	}
	out := &publicv1.ListSessionsResponse{Sessions: make([]*publicv1.Session, 0, len(sessions))}
	for _, sess := range sessions {
		out.Sessions = append(out.Sessions, sessionToProto(sess))
	}
	return out, nil
}

func (s *Sessions) GetSession(
	ctx context.Context,
	req *publicv1.GetSessionRequest,
) (*publicv1.GetSessionResponse, error) {
	org, err := requireOrg(ctx, authz.CapManage)
	if err != nil {
		return nil, err
	}
	sess, err := s.Sessions.Get(ctx, org, req.GetSessionId())
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.GetSessionResponse{Session: sessionToProto(sess)}, nil
}

func (s *Sessions) DeleteSession(
	ctx context.Context,
	req *publicv1.DeleteSessionRequest,
) (*publicv1.DeleteSessionResponse, error) {
	org, err := requireOrg(ctx, authz.CapManage)
	if err != nil {
		return nil, err
	}
	if err := s.Sessions.Delete(ctx, org, req.GetSessionId()); err != nil {
		return nil, Status(err)
	}
	return &publicv1.DeleteSessionResponse{}, nil
}

// lifecycle runs one no-payload action and returns the refreshed row — the
// same shape as the REST :start/:stop/:restart/:logout actions.
func (s *Sessions) lifecycle(
	ctx context.Context,
	sessionID string,
	run func(context.Context, string, string) error,
) (*publicv1.Session, error) {
	org, err := requireOrg(ctx, authz.CapManage)
	if err != nil {
		return nil, err
	}
	if err := run(ctx, org, sessionID); err != nil {
		return nil, Status(err)
	}
	sess, err := s.Sessions.Get(ctx, org, sessionID)
	if err != nil {
		return nil, Status(err)
	}
	return sessionToProto(sess), nil
}

func (s *Sessions) StartSession(
	ctx context.Context,
	req *publicv1.StartSessionRequest,
) (*publicv1.StartSessionResponse, error) {
	sess, err := s.lifecycle(ctx, req.GetSessionId(), s.Sessions.Start)
	if err != nil {
		return nil, err
	}
	return &publicv1.StartSessionResponse{Session: sess}, nil
}

func (s *Sessions) StopSession(
	ctx context.Context,
	req *publicv1.StopSessionRequest,
) (*publicv1.StopSessionResponse, error) {
	sess, err := s.lifecycle(ctx, req.GetSessionId(), s.Sessions.Stop)
	if err != nil {
		return nil, err
	}
	return &publicv1.StopSessionResponse{Session: sess}, nil
}

func (s *Sessions) RestartSession(
	ctx context.Context,
	req *publicv1.RestartSessionRequest,
) (*publicv1.RestartSessionResponse, error) {
	sess, err := s.lifecycle(ctx, req.GetSessionId(), s.Sessions.Restart)
	if err != nil {
		return nil, err
	}
	return &publicv1.RestartSessionResponse{Session: sess}, nil
}

func (s *Sessions) LogoutSession(
	ctx context.Context,
	req *publicv1.LogoutSessionRequest,
) (*publicv1.LogoutSessionResponse, error) {
	sess, err := s.lifecycle(ctx, req.GetSessionId(), s.Sessions.Logout)
	if err != nil {
		return nil, err
	}
	return &publicv1.LogoutSessionResponse{Session: sess}, nil
}

func (s *Sessions) GetSessionQrCode(
	ctx context.Context,
	req *publicv1.GetSessionQrCodeRequest,
) (*publicv1.GetSessionQrCodeResponse, error) {
	org, err := requireOrg(ctx, authz.CapManage)
	if err != nil {
		return nil, err
	}
	qr, err := s.Sessions.QR(ctx, org, req.GetSessionId())
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.GetSessionQrCodeResponse{QrCode: qrToProto(qr)}, nil
}

func (s *Sessions) CreatePairingCode(
	ctx context.Context,
	req *publicv1.CreatePairingCodeRequest,
) (*publicv1.CreatePairingCodeResponse, error) {
	org, err := requireOrg(ctx, authz.CapManage)
	if err != nil {
		return nil, err
	}
	code, err := s.Sessions.PairingCode(ctx, org, req.GetSessionId(), req.GetPhone())
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.CreatePairingCodeResponse{Code: code}, nil
}

func (s *Sessions) GetMe(ctx context.Context, req *publicv1.GetMeRequest) (*publicv1.GetMeResponse, error) {
	org, err := requireOrg(ctx, authz.CapManage)
	if err != nil {
		return nil, err
	}
	me, err := s.Sessions.Me(ctx, org, req.GetSessionId())
	if err != nil {
		return nil, Status(err)
	}
	return &publicv1.GetMeResponse{Me: meToProto(me)}, nil
}

// ---------------------------------------------------------------------------
// translation helpers
// ---------------------------------------------------------------------------

func sessionStatusToProto(s domain.SessionStatus) publicv1.SessionStatus {
	switch s {
	case domain.SessionStarting:
		return publicv1.SessionStatus_SESSION_STATUS_STARTING
	case domain.SessionScanQR:
		return publicv1.SessionStatus_SESSION_STATUS_SCAN_QR_CODE
	case domain.SessionWorking:
		return publicv1.SessionStatus_SESSION_STATUS_WORKING
	case domain.SessionFailed:
		return publicv1.SessionStatus_SESSION_STATUS_FAILED
	case domain.SessionStopped:
		return publicv1.SessionStatus_SESSION_STATUS_STOPPED
	case domain.SessionLoggedOut:
		return publicv1.SessionStatus_SESSION_STATUS_LOGGED_OUT
	default:
		return publicv1.SessionStatus_SESSION_STATUS_UNSPECIFIED
	}
}

func sessionToProto(s domain.WASession) *publicv1.Session {
	return &publicv1.Session{
		Id:                    s.ID,
		OrganizationId:        s.OrganizationID,
		CreatedByUserId:       s.CreatedByUserID,
		GatewayId:             s.GatewayID,
		Label:                 s.Label,
		Status:                sessionStatusToProto(s.Status),
		WaJid:                 s.WAJID,
		WaLid:                 s.WALID,
		PhoneNumber:           s.PhoneNumber,
		IsAdminSession:        s.IsAdminSession,
		AutoRead:              s.AutoRead,
		PresenceTyping:        s.PresenceTyping,
		RatePerMin:            int32(s.RatePerMin),
		RatePerHour:           int32(s.RatePerHour),
		LastConnectedAtUnixMs: s.LastConnectedAt,
		CreatedAtUnixMs:       s.CreatedAt,
		UpdatedAtUnixMs:       s.UpdatedAt,
	}
}

func meToProto(m service.Me) *publicv1.Me {
	return &publicv1.Me{
		SessionId:   m.SessionID,
		Status:      sessionStatusToProto(m.Status),
		WaJid:       m.WAJID,
		WaLid:       m.WALID,
		PhoneNumber: m.PhoneNumber,
		Connected:   m.Connected,
	}
}

func qrToProto(qr service.QR) *publicv1.QrCode {
	var expires *int64
	if qr.ExpiresAt != 0 {
		v := qr.ExpiresAt
		expires = &v
	}
	return &publicv1.QrCode{Code: qr.Code, ExpiresAtUnixMs: expires}
}

// requireOrg is humax.Org plus the capability gate, expressed as a gRPC error:
// no principal → Unauthenticated; missing capability → PermissionDenied; then
// the principal's organization scopes every downstream service call. Org comes
// only from the authenticated principal, never from request payloads.
func requireOrg(ctx context.Context, cap authz.Capability) (string, error) {
	p := authz.FromContext(ctx)
	if p == nil {
		return "", status.Error(codes.Unauthenticated, "authentication required")
	}
	if !authz.Allow(p, cap) {
		return "", status.Error(codes.PermissionDenied, "missing required capability: "+string(cap))
	}
	if p.OrganizationID == "" {
		return "", status.Error(codes.Unauthenticated, "authentication required")
	}
	return p.OrganizationID, nil
}
