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
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
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
	GatewayID, CertificateID, AuthorityID, SerialNumber string
	Fingerprint                                         []byte
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
	err := s.db.QueryRowContext(ctx, `SELECT c.id, c.authority_id FROM gateways g JOIN gateway_certificates c ON c.gateway_id=g.id
		WHERE g.id=? AND g.deleted_at IS NULL AND g.status<>'disabled'
		AND c.serial_number=? AND c.certificate_fingerprint=? AND c.revoked_at IS NULL
		AND c.not_before<=? AND c.not_after>? LIMIT 1`, identity.GatewayID, identity.SerialNumber, identity.Fingerprint, now, now).Scan(&identity.CertificateID, &identity.AuthorityID)
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
	renewal renewalIssuer
}

type renewalIssuer interface {
	Renew(context.Context, service.RenewalInput) (service.EnrollmentResult, error)
}

func (h gatewayEnrollmentGRPC) Renew(ctx context.Context, req *gatewayv1.GatewayEnrollmentServiceRenewRequest) (*gatewayv1.GatewayEnrollmentServiceRenewResponse, error) {
	identity, ok := gatewayIdentityFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "renewal authentication failed")
	}
	if h.renewal == nil {
		return nil, status.Error(codes.Internal, "renewal unavailable")
	}
	if req == nil || len(req.CsrDer) == 0 || len(req.CsrDer) > pki.MaxCSRBytes {
		return nil, status.Error(codes.InvalidArgument, "invalid renewal request")
	}
	result, err := h.renewal.Renew(ctx, service.RenewalInput{Credential: service.RenewalCredential{GatewayID: identity.GatewayID, CertificateID: identity.CertificateID, SerialNumber: identity.SerialNumber, Fingerprint: identity.Fingerprint}, CSRDER: req.CsrDer})
	if err != nil {
		return nil, renewalStatus(err)
	}
	return &gatewayv1.GatewayEnrollmentServiceRenewResponse{GatewayId: result.GatewayID, CertificateChainPem: []byte(result.CertificatePEM), TrustBundlePem: []byte(result.TrustBundlePEM), AuthorityId: result.AuthorityID, SerialNumber: result.SerialNumber, NotBeforeUnixMs: result.NotBefore, NotAfterUnixMs: result.NotAfter}, nil
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

func renewalStatus(err error) error {
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "renewal canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "renewal deadline exceeded")
	}
	var invalid *service.InvalidCredentialError
	var transient *service.TransientError
	switch {
	case errors.As(err, &invalid):
		return status.Error(codes.Unauthenticated, "renewal authentication failed")
	case errors.As(err, &transient):
		return status.Error(codes.Unavailable, "renewal temporarily unavailable")
	default:
		return status.Error(codes.Internal, "renewal failed")
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
	HeartbeatForEpoch(context.Context, domain.GatewayHeartbeat, int64) (domain.GatewayAcceptedConnection, bool, error)
	SetStatusForEpoch(context.Context, domain.GatewayLifecycleReport, int64) (bool, error)
	DisconnectForEpoch(context.Context, domain.GatewayConnection, int64) (bool, error)
	DesiredStateForEpoch(context.Context, domain.GatewayConnection, uint64, int64) ([]domain.GatewayDesiredSession, bool, error)
	AcknowledgeDesiredStateForEpoch(context.Context, domain.GatewayConnection, uint64, int64) (bool, error)
}

type gatewayControlStore struct {
	repo           gatewayControlRepo
	reconciliation *store.GatewayReconciliationRepo
	eventIngest    *store.GatewayEventIngestRepo
	now            func() time.Time
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
	baseURL := optionalString(hello.HTTPBaseURL)
	accepted, err := s.repo.AcceptConnection(ctx, domain.GatewayConnectionHello{
		GatewayID: gatewayID, BaseURL: baseURL, GRPCEndpoint: endpoint, SoftwareVersion: version,
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
	desiredLifecycle, ok := desiredLifecycleForStoredValue(accepted.DesiredLifecycle)
	if !ok {
		return apigateway.Connection{}, apigateway.ErrConflict
	}
	return apigateway.Connection{
		ID: domain.NewULID(), Epoch: accepted.ConnectionEpoch, HeartbeatInterval: apigateway.DefaultHeartbeatInterval,
		LeaseTimeout:     apigateway.DefaultLeaseTimeout,
		DesiredLifecycle: desiredLifecycle,
		DesiredRevision:  accepted.DesiredRevision,
	}, nil
}

func (s gatewayControlStore) Heartbeat(ctx context.Context, gatewayID string, epoch uint64, heartbeat apigateway.Heartbeat) (apigateway.DesiredLifecycle, error) {
	runtimeStatus, valid := runtimeGatewayStatus(heartbeat.RuntimeState)
	if !valid {
		return apigateway.DesiredLifecycle{}, apigateway.ErrConflict
	}
	desired, applied, err := s.repo.HeartbeatForEpoch(ctx, domain.GatewayHeartbeat{
		GatewayConnection: domain.GatewayConnection{GatewayID: gatewayID, ConnectionEpoch: epoch},
		SessionCount:      int(heartbeat.SessionCount),
		Status:            runtimeStatus,
		JournalState:      journalStateString(heartbeat.JournalState),
		JournalEntries:    journalCount(heartbeat.JournalState, heartbeat.JournalEntries),
		JournalBytes:      journalCount(heartbeat.JournalState, heartbeat.JournalBytes),
	}, s.clock().UnixMilli())
	if err = fencedStoreResult(applied, err); err != nil {
		return apigateway.DesiredLifecycle{}, err
	}
	action, ok := desiredLifecycleForStoredValue(desired.DesiredLifecycle)
	if !ok || desired.ConnectionEpoch != epoch {
		return apigateway.DesiredLifecycle{}, apigateway.ErrConflict
	}
	return apigateway.DesiredLifecycle{Action: action, Revision: desired.DesiredRevision}, nil
}

// journalStateString maps the wire journal state onto the stored enum; UNKNOWN
// means "no report this cycle" and persists nothing.
func journalStateString(state gatewayv1.GatewayJournalState) *string {
	switch state {
	case gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_HEALTHY:
		return strPtr("healthy")
	case gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_DEGRADED:
		return strPtr("degraded")
	case gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_PAUSED:
		return strPtr("paused")
	case gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_CRITICAL:
		return strPtr("critical")
	default:
		return nil
	}
}

// journalCount carries a telemetry count only alongside a reported state.
func journalCount(state gatewayv1.GatewayJournalState, value uint64) *uint64 {
	if state == gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_UNKNOWN {
		return nil
	}
	v := value
	return &v
}

func strPtr(s string) *string { return &s }

func (s gatewayControlStore) DesiredState(ctx context.Context, gatewayID string, epoch, revision uint64, leaseExpiresAt time.Time) (apigateway.DesiredState, error) {
	assignments, current, err := s.repo.DesiredStateForEpoch(ctx, domain.GatewayConnection{GatewayID: gatewayID, ConnectionEpoch: epoch}, revision, leaseExpiresAt.UnixMilli())
	if err = fencedStoreResult(current, err); err != nil {
		return apigateway.DesiredState{}, err
	}
	out := apigateway.DesiredState{Revision: revision, Assignments: make([]apigateway.DesiredSession, 0, len(assignments))}
	for _, assignment := range assignments {
		out.Assignments = append(out.Assignments, apigateway.DesiredSession{
			SessionID: assignment.SessionID, OrganizationID: assignment.OrganizationID, DeviceJID: assignment.DeviceJID, AssignmentEpoch: assignment.AssignmentEpoch,
			DesiredAction:  sessionDesiredAction(assignment.DesiredRun),
			ConfigRevision: assignment.ConfigRevision, AutoRead: assignment.AutoRead, PresenceTyping: assignment.PresenceTyping,
			RatePerMin: assignment.RatePerMin, RatePerHour: assignment.RatePerHour,
			LeaseExpiresAt: time.UnixMilli(assignment.LeaseExpiresAt).UTC(),
		})
	}
	return out, nil
}

func sessionDesiredAction(run bool) gatewayv1.SessionDesiredAction {
	if run {
		return gatewayv1.SessionDesiredAction_SESSION_DESIRED_ACTION_RUN
	}
	return gatewayv1.SessionDesiredAction_SESSION_DESIRED_ACTION_STOP
}

func (s gatewayControlStore) PersistDesiredStateReport(ctx context.Context, gatewayID string, epoch uint64, report apigateway.ReconciliationReport) error {
	if s.reconciliation == nil {
		return apigateway.ErrUnavailable
	}
	results := make([]store.GatewayReconciliationResult, 0, len(report.Results))
	for _, r := range report.Results {
		results = append(results, store.GatewayReconciliationResult{SessionID: r.SessionID, AssignmentEpoch: r.AssignmentEpoch, DeviceJID: r.DeviceJID, Status: r.Status})
	}
	err := s.reconciliation.Persist(ctx, store.GatewayReconciliationReport{GatewayID: gatewayID, Epoch: epoch, Revision: report.Revision, KeystoreState: report.KeystoreState, KeystoreBytes: report.KeystoreBytes, CheckedAt: report.CheckedAt, LocalDevices: report.LocalDevices, Results: results}, s.clock().UnixMilli())
	if err != nil {
		return fmt.Errorf("%w: %w", apigateway.ErrUnavailable, err)
	}
	return nil
}

func (s gatewayControlStore) IngestEvents(ctx context.Context, gatewayID string, epoch uint64, events []apigateway.GatewayEvent) error {
	if s.eventIngest == nil {
		return apigateway.ErrUnavailable
	}
	batch := make([]store.GatewayEvent, 0, len(events))
	for _, event := range events {
		batch = append(batch, store.GatewayEvent{EventID: event.EventID, GatewayID: gatewayID, SessionID: event.SessionID, OrganizationID: event.OrganizationID, Type: event.Type, ConnectionEpoch: epoch, AssignmentEpoch: event.AssignmentEpoch, Payload: event.Payload, OccurredAt: event.OccurredAt.UnixMilli()})
	}
	if err := s.eventIngest.IngestBatch(ctx, batch, s.clock().UnixMilli()); err != nil {
		return fmt.Errorf("%w: %w", apigateway.ErrUnavailable, err)
	}
	return nil
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

func (s gatewayControlStore) Disconnect(ctx context.Context, gatewayID string, epoch uint64) error {
	applied, err := s.repo.DisconnectForEpoch(ctx, domain.GatewayConnection{
		GatewayID: gatewayID, ConnectionEpoch: epoch,
	}, s.clock().UnixMilli())
	return fencedStoreResult(applied, err)
}

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

func desiredLifecycleForStoredValue(desired string) (gatewayv1.LifecycleDirectiveAction, bool) {
	switch desired {
	case "drain":
		return gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN, true
	case "run":
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

func newPrivateGatewayGRPCServer(tlsConfig *tls.Config, auth privateGatewayAuthenticator, enrollment enrollmentRedeemer, renewal renewalIssuer, readiness func() error, control gatewayv1.GatewayControlServiceServer) *grpc.Server {
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)), grpc.UnaryInterceptor(auth.unary), grpc.StreamInterceptor(auth.stream))
	gatewayv1.RegisterGatewayEnrollmentServiceServer(server, gatewayEnrollmentGRPC{service: enrollment, renewal: renewal})
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

// sessionDesiredController implements service.SessionDesiredController over
// the assignment repo: flipping desired run state advances the owning
// gateway's revision so its reconciler starts or stops the session.
type sessionDesiredController struct{ assignments *store.GatewayAssignmentRepo }

func (c sessionDesiredController) SetSessionDesired(ctx context.Context, sessionID string, run bool) error {
	return c.assignments.SetSessionDesired(ctx, sessionID, run, time.Now().UnixMilli())
}
