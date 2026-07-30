package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	apigateway "github.com/ramaadi/quick-whatsapp-gateway/internal/api/gateway"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki/apiidentity"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const (
	enrollMethod            = "/gateway.v1.GatewayEnrollmentService/Enroll"
	maxEnrollmentTokenBytes = 256
)

type gatewayIdentity struct {
	GatewayID, AuthorityID, SerialNumber string
	Fingerprint                          []byte
}
type gatewayIdentityKey struct{}

func gatewayIdentityFromContext(ctx context.Context) (gatewayIdentity, bool) {
	v, ok := ctx.Value(gatewayIdentityKey{}).(gatewayIdentity)
	return v, ok
}

type gatewayCredentialStore interface {
	AuthorizeGatewayCertificate(context.Context, gatewayIdentity, int64) (gatewayIdentity, error)
}

type mysqlGatewayCredentialStore struct{ db *sql.DB }

func (s mysqlGatewayCredentialStore) AuthorizeGatewayCertificate(ctx context.Context, identity gatewayIdentity, now int64) (gatewayIdentity, error) {
	err := s.db.QueryRowContext(ctx, `SELECT c.authority_id FROM gateways g JOIN gateway_certificates c ON c.gateway_id=g.id
		WHERE g.id=? AND g.deleted_at IS NULL AND g.status<>'disabled'
		AND c.serial_number=? AND c.certificate_fingerprint=? AND c.revoked_at IS NULL
		AND c.not_before<=? AND c.not_after>? LIMIT 1`, identity.GatewayID, identity.SerialNumber, identity.Fingerprint, now, now).Scan(&identity.AuthorityID)
	return identity, err
}

type privateGatewayAuthenticator struct {
	store gatewayCredentialStore
	now   func() time.Time
}

func (a privateGatewayAuthenticator) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	authenticated, err := a.authenticate(ctx, info.FullMethod == enrollMethod)
	if err != nil {
		return nil, err
	}
	return handler(authenticated, req)
}

func (a privateGatewayAuthenticator) stream(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	authenticated, err := a.authenticate(stream.Context(), info.FullMethod == enrollMethod)
	if err != nil {
		return err
	}
	return handler(srv, &contextServerStream{ServerStream: stream, ctx: authenticated})
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextServerStream) Context() context.Context { return s.ctx }

func (a privateGatewayAuthenticator) authenticate(ctx context.Context, allowAbsent bool) (context.Context, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		if allowAbsent {
			return ctx, nil
		}
		return nil, status.Error(codes.Unauthenticated, "gateway authentication required")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		if allowAbsent {
			return ctx, nil
		}
		return nil, status.Error(codes.Unauthenticated, "gateway authentication required")
	}
	if len(tlsInfo.State.VerifiedChains) == 0 {
		return nil, status.Error(codes.Unauthenticated, "gateway authentication failed")
	}
	identity, err := strictGatewayIdentity(tlsInfo.State.PeerCertificates[0])
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "gateway authentication failed")
	}
	if a.store == nil {
		return nil, status.Error(codes.Unauthenticated, "gateway authentication failed")
	}
	identity, err = a.store.AuthorizeGatewayCertificate(ctx, identity, a.clock().UnixMilli())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "gateway authentication failed")
	}
	return context.WithValue(ctx, gatewayIdentityKey{}, identity), nil
}

func (a privateGatewayAuthenticator) clock() time.Time {
	if a.now != nil {
		return a.now().UTC()
	}
	return time.Now().UTC()
}

func strictGatewayIdentity(cert *x509.Certificate) (gatewayIdentity, error) {
	if cert == nil || cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage != x509.KeyUsageDigitalSignature || len(cert.URIs) != 1 || len(cert.DNSNames) != 0 || len(cert.IPAddresses) != 0 || len(cert.EmailAddresses) != 0 {
		return gatewayIdentity{}, errors.New("invalid gateway certificate policy")
	}
	if len(cert.ExtKeyUsage) != 2 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || cert.ExtKeyUsage[1] != x509.ExtKeyUsageServerAuth {
		return gatewayIdentity{}, errors.New("invalid gateway certificate usage")
	}
	const prefix = "spiffe://quick-wa/gateway/"
	uri := cert.URIs[0].String()
	if !strings.HasPrefix(uri, prefix) {
		return gatewayIdentity{}, errors.New("invalid gateway identity")
	}
	id, err := url.PathUnescape(strings.TrimPrefix(uri, prefix))
	if err != nil || id != strings.TrimPrefix(uri, prefix) || pki.ValidateGatewayID(id) != nil {
		return gatewayIdentity{}, errors.New("invalid gateway identity")
	}
	if cert.SerialNumber == nil || cert.SerialNumber.Sign() <= 0 {
		return gatewayIdentity{}, errors.New("invalid serial")
	}
	if len(cert.Raw) == 0 || len(cert.RawIssuer) == 0 {
		return gatewayIdentity{}, errors.New("invalid issuer")
	}
	fingerprint := sha256.Sum256(cert.Raw)
	return gatewayIdentity{GatewayID: id, SerialNumber: cert.SerialNumber.String(), Fingerprint: fingerprint[:]}, nil
}

type enrollmentRedeemer interface {
	RedeemWithInput(context.Context, service.RedeemInput) (service.EnrollmentResult, error)
}

type gatewayEnrollmentGRPC struct {
	gatewayv1.UnimplementedGatewayEnrollmentServiceServer
	service enrollmentRedeemer
}

func (h gatewayEnrollmentGRPC) Enroll(ctx context.Context, req *gatewayv1.GatewayEnrollmentServiceEnrollRequest) (*gatewayv1.GatewayEnrollmentServiceEnrollResponse, error) {
	if req == nil || len(req.Token) == 0 || len(req.Token) > maxEnrollmentTokenBytes || len(req.CsrDer) == 0 || len(req.CsrDer) > pki.MaxCSRBytes {
		return nil, status.Error(codes.InvalidArgument, "invalid enrollment request")
	}
	result, err := h.service.RedeemWithInput(ctx, service.RedeemInput{Token: req.Token, CSRDER: req.CsrDer})
	if err != nil {
		return nil, enrollmentStatus(err)
	}
	return &gatewayv1.GatewayEnrollmentServiceEnrollResponse{GatewayId: result.GatewayID, CertificateChainPem: []byte(result.CertificatePEM), TrustBundlePem: []byte(result.TrustBundlePEM), AuthorityId: result.AuthorityID, SerialNumber: result.SerialNumber, NotBeforeUnixMs: result.NotBefore, NotAfterUnixMs: result.NotAfter}, nil
}

func enrollmentStatus(err error) error {
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "enrollment canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "enrollment deadline exceeded")
	}
	var invalid *service.InvalidCredentialError
	var limited *service.RateLimitedError
	var progress *service.InProgressError
	var conflict *service.StateConflictError
	var transient *service.TransientError
	switch {
	case errors.As(err, &invalid):
		return status.Error(codes.Unauthenticated, "enrollment authentication failed")
	case errors.As(err, &limited):
		return status.Error(codes.ResourceExhausted, "enrollment rate limited")
	case errors.As(err, &progress):
		return status.Error(codes.Aborted, "enrollment in progress")
	case errors.As(err, &conflict):
		return status.Error(codes.Aborted, "enrollment state conflict")
	case errors.As(err, &transient):
		return status.Error(codes.Unavailable, "enrollment temporarily unavailable")
	default:
		return status.Error(codes.Internal, "enrollment failed")
	}
}

type privateGatewayHealth struct {
	gatewayv1.UnimplementedGatewayHealthServiceServer
	readiness func() error
}

func (h privateGatewayHealth) Check(context.Context, *gatewayv1.GatewayHealthServiceCheckRequest) (*gatewayv1.GatewayHealthServiceCheckResponse, error) {
	serving := gatewayv1.ServingStatus_SERVING_STATUS_SERVING
	if h.readiness != nil && h.readiness() != nil {
		serving = gatewayv1.ServingStatus_SERVING_STATUS_NOT_SERVING
	}
	return &gatewayv1.GatewayHealthServiceCheckResponse{Status: serving}, nil
}

type gatewayControlRepo interface {
	AcceptConnection(context.Context, domain.GatewayConnectionHello, int64) (domain.GatewayAcceptedConnection, error)
	HeartbeatForEpoch(context.Context, domain.GatewayHeartbeat, int64) (bool, error)
	SetStatusForEpoch(context.Context, domain.GatewayLifecycleReport, int64) (bool, error)
}

type gatewayControlStore struct {
	repo gatewayControlRepo
	now  func() time.Time
}

func (s gatewayControlStore) Accept(ctx context.Context, gatewayID string, hello apigateway.Hello) (apigateway.Connection, error) {
	runtimeStatus, ok := runtimeGatewayStatus(hello.RuntimeState)
	if !ok {
		return apigateway.Connection{}, apigateway.ErrConflict
	}
	capabilities, err := json.Marshal(hello.Capabilities)
	if err != nil {
		return apigateway.Connection{}, err
	}
	endpoint, version := optionalString(hello.GRPCEndpoint), optionalString(hello.SoftwareVersion)
	accepted, err := s.repo.AcceptConnection(ctx, domain.GatewayConnectionHello{
		GatewayID: gatewayID, GRPCEndpoint: endpoint, SoftwareVersion: version,
		Capabilities: capabilities, SessionCount: int(hello.SessionCount), Status: runtimeStatus,
	}, s.clock().UnixMilli())
	if err != nil {
		var apiErr *domain.APIError
		hasAPIError := errors.As(err, &apiErr)
		if errors.Is(err, sql.ErrNoRows) || (hasAPIError && apiErr.Code == domain.CodeNotFound) {
			return apigateway.Connection{}, apigateway.ErrUnauthorized
		}
		if hasAPIError && apiErr.Code == domain.CodeConflict {
			return apigateway.Connection{}, apigateway.ErrConflict
		}
		return apigateway.Connection{}, fmt.Errorf("%w: %w", apigateway.ErrUnavailable, err)
	}
	desiredLifecycle, ok := desiredLifecycleForStatus(accepted.Status)
	if !ok {
		return apigateway.Connection{}, apigateway.ErrConflict
	}
	return apigateway.Connection{
		ID: domain.NewULID(), Epoch: accepted.ConnectionEpoch, HeartbeatInterval: 5 * time.Second,
		LeaseTimeout:     15 * time.Second,
		DesiredLifecycle: desiredLifecycle,
	}, nil
}

func (s gatewayControlStore) Heartbeat(ctx context.Context, gatewayID string, epoch uint64, heartbeat apigateway.Heartbeat) error {
	runtimeStatus, valid := runtimeGatewayStatus(heartbeat.RuntimeState)
	if !valid {
		return apigateway.ErrConflict
	}
	applied, err := s.repo.HeartbeatForEpoch(ctx, domain.GatewayHeartbeat{
		GatewayConnection: domain.GatewayConnection{GatewayID: gatewayID, ConnectionEpoch: epoch},
		SessionCount:      int(heartbeat.SessionCount),
		Status:            runtimeStatus,
	}, s.clock().UnixMilli())
	return fencedStoreResult(applied, err)
}

func (s gatewayControlStore) Lifecycle(ctx context.Context, gatewayID string, epoch uint64, report apigateway.LifecycleReport) error {
	lifecycle, ok := runtimeGatewayStatus(report.State)
	if !ok {
		return apigateway.ErrConflict
	}
	updated, err := s.repo.SetStatusForEpoch(ctx, domain.GatewayLifecycleReport{
		GatewayConnection: domain.GatewayConnection{GatewayID: gatewayID, ConnectionEpoch: epoch},
		Status:            lifecycle,
	}, s.clock().UnixMilli())
	return fencedStoreResult(updated, err)
}

// Disconnect is currently observational only. The persisted lease expires from
// the last fenced heartbeat; a replacement stream immediately advances epoch.
func (gatewayControlStore) Disconnect(context.Context, string, uint64) error { return nil }

func (s gatewayControlStore) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func fencedStoreResult(updated bool, err error) error {
	if err != nil {
		return fmt.Errorf("%w: %w", apigateway.ErrUnavailable, err)
	}
	if !updated {
		return apigateway.ErrStaleEpoch
	}
	return nil
}

func desiredLifecycleForStatus(status domain.GatewayStatus) (gatewayv1.LifecycleDirectiveAction, bool) {
	switch status {
	case domain.GatewayDraining, domain.GatewayDrained:
		return gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN, true
	case domain.GatewayJoining, domain.GatewayActive, domain.GatewayDegraded:
		return gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN, true
	default:
		return gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_UNKNOWN, false
	}
}

func runtimeGatewayStatus(state gatewayv1.GatewayRuntimeState) (domain.GatewayStatus, bool) {
	switch state {
	case gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_STARTING:
		return domain.GatewayJoining, true
	case gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY:
		return domain.GatewayActive, true
	case gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING:
		return domain.GatewayDraining, true
	case gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED:
		return domain.GatewayDrained, true
	case gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED:
		return domain.GatewayDegraded, true
	default:
		return "", false
	}
}

func newPrivateGatewayGRPCServer(tlsConfig *tls.Config, auth privateGatewayAuthenticator, enrollment enrollmentRedeemer, readiness func() error, control gatewayv1.GatewayControlServiceServer) *grpc.Server {
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)), grpc.UnaryInterceptor(auth.unary), grpc.StreamInterceptor(auth.stream))
	gatewayv1.RegisterGatewayEnrollmentServiceServer(server, gatewayEnrollmentGRPC{service: enrollment})
	gatewayv1.RegisterGatewayHealthServiceServer(server, privateGatewayHealth{readiness: readiness})
	if control != nil {
		gatewayv1.RegisterGatewayControlServiceServer(server, control)
	}
	return server
}

func privateGatewayTLSConfig(manager *apiidentity.Manager, clientRoots *x509.CertPool) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: manager.GetCertificate, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: clientRoots}
}

func renewAPIIdentity(ctx context.Context, manager *apiidentity.Manager, renewBefore time.Duration, log *slog.Logger) {
	interval := renewBefore / 2
	if interval > time.Hour {
		interval = time.Hour
	}
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := manager.Ensure(ctx); err != nil && ctx.Err() == nil {
				log.Error("API TLS identity renewal failed", "err", err, "current_expiry", manager.Expiry())
			}
		}
	}
}
