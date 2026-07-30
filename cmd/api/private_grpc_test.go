package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"testing"
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

type fakeCredentialStore struct {
	identity gatewayIdentity
	err      error
	calls    int
}

func (s *fakeCredentialStore) AuthorizeGatewayCertificate(context.Context, gatewayIdentity, int64) (gatewayIdentity, error) {
	s.calls++
	return s.identity, s.err
}

func gatewayLeaf(t *testing.T, mutate func(*x509.Certificate)) *x509.Certificate {
	t.Helper()
	u, _ := url.Parse("spiffe://quick-wa/gateway/gw_1")
	c := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "gw_1"}, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, URIs: []*url.URL{u}, Raw: []byte{2}, RawIssuer: []byte{1}}
	if mutate != nil {
		mutate(c)
	}
	return c
}

func tlsPeerContext(cert *x509.Certificate, verified bool) context.Context {
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	if verified {
		state.VerifiedChains = [][]*x509.Certificate{{cert}}
	}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})
}

func TestPrivateAuthenticationMatrix(t *testing.T) {
	store := &fakeCredentialStore{identity: gatewayIdentity{GatewayID: "gw_1", AuthorityID: "ca_1", SerialNumber: "7"}}
	auth := privateGatewayAuthenticator{store: store, now: func() time.Time { return time.Unix(100, 0) }}
	handler := func(ctx context.Context, _ any) (any, error) {
		id, _ := gatewayIdentityFromContext(ctx)
		return id, nil
	}

	if _, err := auth.unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: enrollMethod}, handler); err != nil {
		t.Fatalf("anonymous enroll rejected: %v", err)
	}
	for _, method := range []string{"/gateway.v1.GatewayHealthService/Check", "/unknown.Service/Call"} {
		if _, err := auth.unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method}, handler); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("anonymous %s = %v", method, err)
		}
	}
	response, err := auth.unary(tlsPeerContext(gatewayLeaf(t, nil), true), nil, &grpc.UnaryServerInfo{FullMethod: "/unknown.Service/Call"}, handler)
	if err != nil || response.(gatewayIdentity).AuthorityID != "ca_1" {
		t.Fatalf("authenticated unary = (%v, %v)", response, err)
	}
	if _, err = auth.unary(tlsPeerContext(gatewayLeaf(t, nil), false), nil, &grpc.UnaryServerInfo{FullMethod: enrollMethod}, handler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unverified presented cert = %v", err)
	}

	stream := &testServerStream{ctx: context.Background()}
	if err = auth.stream(nil, stream, &grpc.StreamServerInfo{FullMethod: "/unknown.Service/Stream"}, func(any, grpc.ServerStream) error { return nil }); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous stream = %v", err)
	}
	stream.ctx = tlsPeerContext(gatewayLeaf(t, nil), true)
	if err = auth.stream(nil, stream, &grpc.StreamServerInfo{FullMethod: "/unknown.Service/Stream"}, func(_ any, got grpc.ServerStream) error {
		_, ok := gatewayIdentityFromContext(got.Context())
		if !ok {
			t.Fatal("stream identity missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type testServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *testServerStream) Context() context.Context { return s.ctx }

func TestPrivateAuthenticationRejectsPolicyAndControlPlaneState(t *testing.T) {
	mutations := map[string]func(*x509.Certificate){
		"CA":            func(c *x509.Certificate) { c.IsCA = true },
		"DNS":           func(c *x509.Certificate) { c.DNSNames = []string{"gateway"} },
		"wrong EKU":     func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} },
		"ambiguous URI": func(c *x509.Certificate) { c.URIs = append(c.URIs, c.URIs[0]) },
		"bad ID":        func(c *x509.Certificate) { c.URIs[0], _ = url.Parse("spiffe://quick-wa/gateway/bad%2Fid") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := strictGatewayIdentity(gatewayLeaf(t, mutate)); err == nil {
				t.Fatal("invalid leaf accepted")
			}
		})
	}
	for _, state := range []string{"disabled", "deleted", "revoked", "expired", "unknown"} {
		t.Run(state, func(t *testing.T) {
			auth := privateGatewayAuthenticator{store: &fakeCredentialStore{err: errors.New("inactive")}}
			_, err := auth.unary(tlsPeerContext(gatewayLeaf(t, nil), true), nil, &grpc.UnaryServerInfo{FullMethod: "/gateway.v1.GatewayHealthService/Check"}, func(context.Context, any) (any, error) { return nil, nil })
			if status.Code(err) != codes.Unauthenticated || !strings.Contains(err.Error(), "authentication failed") {
				t.Fatalf("state leak/error = %v", err)
			}
		})
	}
}

type fakeRedeemer struct {
	result service.EnrollmentResult
	err    error
	calls  int
}

func (f *fakeRedeemer) RedeemWithInput(context.Context, service.RedeemInput) (service.EnrollmentResult, error) {
	f.calls++
	return f.result, f.err
}

func TestEnrollmentAdapterBoundsMapsAndReturnsExactFields(t *testing.T) {
	redeemer := &fakeRedeemer{result: service.EnrollmentResult{GatewayID: "gw_1", CertificatePEM: "chain", TrustBundlePEM: "root", AuthorityID: "ca_1", SerialNumber: "9", NotBefore: 11, NotAfter: 22}}
	h := gatewayEnrollmentGRPC{service: redeemer}
	response, err := h.Enroll(context.Background(), &gatewayv1.GatewayEnrollmentServiceEnrollRequest{Token: "token", CsrDer: []byte{1}})
	if err != nil || response.GatewayId != "gw_1" || string(response.CertificateChainPem) != "chain" || string(response.TrustBundlePem) != "root" || response.AuthorityId != "ca_1" || response.SerialNumber != "9" || response.NotBeforeUnixMs != 11 || response.NotAfterUnixMs != 22 {
		t.Fatalf("response = (%+v, %v)", response, err)
	}
	for _, req := range []*gatewayv1.GatewayEnrollmentServiceEnrollRequest{{}, {Token: strings.Repeat("s", maxEnrollmentTokenBytes+1), CsrDer: []byte{1}}, {Token: "t", CsrDer: make([]byte, pki.MaxCSRBytes+1)}} {
		if _, err = h.Enroll(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("oversize/empty = %v", err)
		}
	}
	if redeemer.calls != 1 {
		t.Fatalf("redeemer calls = %d", redeemer.calls)
	}

	errorsToCodes := []struct {
		err  error
		code codes.Code
	}{
		{&service.InvalidCredentialError{}, codes.Unauthenticated}, {&service.RateLimitedError{}, codes.ResourceExhausted}, {&service.InProgressError{}, codes.Aborted}, {&service.StateConflictError{}, codes.Aborted}, {&service.TransientError{}, codes.Unavailable}, {context.Canceled, codes.Canceled}, {context.DeadlineExceeded, codes.DeadlineExceeded}, {errors.New("secret token qwg_enroll_v1_NEVER_LEAK"), codes.Internal},
	}
	for _, tt := range errorsToCodes {
		redeemer.err = tt.err
		_, err = h.Enroll(context.Background(), &gatewayv1.GatewayEnrollmentServiceEnrollRequest{Token: "token", CsrDer: []byte{1}})
		if status.Code(err) != tt.code || strings.Contains(err.Error(), "NEVER_LEAK") {
			t.Fatalf("mapping %T = %v", tt.err, err)
		}
	}
}

func TestPrivateServerRegistersOnlyPrivateServices(t *testing.T) {
	server := newPrivateGatewayGRPCServer(&tls.Config{}, privateGatewayAuthenticator{}, &fakeRedeemer{}, nil, nil)
	services := server.GetServiceInfo()
	if len(services) != 2 {
		t.Fatalf("services = %v", services)
	}
	if _, ok := services[gatewayv1.GatewayEnrollmentService_ServiceDesc.ServiceName]; !ok {
		t.Fatal("enrollment absent")
	}
	if _, ok := services[gatewayv1.GatewayHealthService_ServiceDesc.ServiceName]; !ok {
		t.Fatal("health absent")
	}
}

type fakeGatewayControlRepo struct {
	accepted    domain.GatewayAcceptedConnection
	err         error
	heartbeatOK bool
	lifecycleOK bool
	hello       domain.GatewayConnectionHello
	heartbeat   domain.GatewayHeartbeat
	lifecycle   domain.GatewayLifecycleReport
	disconnect  domain.GatewayConnection
}

func (r *fakeGatewayControlRepo) AcceptConnection(_ context.Context, hello domain.GatewayConnectionHello, _ int64) (domain.GatewayAcceptedConnection, error) {
	r.hello = hello
	return r.accepted, r.err
}
func (r *fakeGatewayControlRepo) HeartbeatForEpoch(_ context.Context, heartbeat domain.GatewayHeartbeat, _ int64) (bool, error) {
	r.heartbeat = heartbeat
	return r.heartbeatOK, r.err
}
func (r *fakeGatewayControlRepo) SetStatusForEpoch(_ context.Context, lifecycle domain.GatewayLifecycleReport, _ int64) (bool, error) {
	r.lifecycle = lifecycle
	return r.lifecycleOK, r.err
}
func (r *fakeGatewayControlRepo) DisconnectForEpoch(_ context.Context, connection domain.GatewayConnection, _ int64) (bool, error) {
	r.disconnect = connection
	return r.heartbeatOK, r.err
}

func TestGatewayControlStoreAdaptsAndFencesPersistence(t *testing.T) {
	repo := &fakeGatewayControlRepo{accepted: domain.GatewayAcceptedConnection{ConnectionEpoch: 9, DesiredLifecycle: "run"}, heartbeatOK: true, lifecycleOK: true}
	controlStore := gatewayControlStore{repo: repo, now: func() time.Time { return time.UnixMilli(55) }}
	connection, err := controlStore.Accept(context.Background(), "gw_1", apigateway.Hello{
		SoftwareVersion: "v2", HTTPBaseURL: "https://gateway.test", Capabilities: []gatewayv1.GatewayCapability{gatewayv1.GatewayCapability_GATEWAY_CAPABILITY_SESSION_ENGINE}, SessionCount: 3,
		RuntimeState: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY,
	})
	if err != nil || connection.Epoch != 9 || connection.ID == "" || repo.hello.GatewayID != "gw_1" || repo.hello.BaseURL == nil || *repo.hello.BaseURL != "https://gateway.test" || repo.hello.SessionCount != 3 || repo.hello.Status != domain.GatewayActive {
		t.Fatalf("accept = %+v hello=%+v err=%v", connection, repo.hello, err)
	}
	if err = controlStore.Heartbeat(context.Background(), "gw_1", 9, apigateway.Heartbeat{SessionCount: 4, RuntimeState: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED}); err != nil || repo.heartbeat.ConnectionEpoch != 9 || repo.heartbeat.Status != domain.GatewayDegraded {
		t.Fatalf("heartbeat = %+v err=%v", repo.heartbeat, err)
	}
	if err = controlStore.Lifecycle(context.Background(), "gw_1", 9, apigateway.LifecycleReport{State: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING}); err != nil || repo.lifecycle.Status != domain.GatewayDraining {
		t.Fatalf("lifecycle = %+v err=%v", repo.lifecycle, err)
	}
	if err = controlStore.Disconnect(context.Background(), "gw_1", 9); err != nil || repo.disconnect.GatewayID != "gw_1" || repo.disconnect.ConnectionEpoch != 9 {
		t.Fatalf("disconnect = %+v err=%v", repo.disconnect, err)
	}
	repo.heartbeatOK = false
	if err = controlStore.Heartbeat(context.Background(), "gw_1", 9, apigateway.Heartbeat{RuntimeState: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY}); !errors.Is(err, apigateway.ErrStaleEpoch) {
		t.Fatalf("stale heartbeat error = %v", err)
	}
	if err = controlStore.Disconnect(context.Background(), "gw_1", 9); !errors.Is(err, apigateway.ErrStaleEpoch) {
		t.Fatalf("stale disconnect error = %v", err)
	}
	repo.err = domain.ErrNotFound("connectable gateway not found")
	if _, err = controlStore.Accept(context.Background(), "gw_disabled", apigateway.Hello{RuntimeState: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_STARTING}); !errors.Is(err, apigateway.ErrUnauthorized) {
		t.Fatalf("disabled gateway error = %v", err)
	}
	if _, err = controlStore.Accept(context.Background(), "gw_1", apigateway.Hello{}); !errors.Is(err, apigateway.ErrConflict) {
		t.Fatalf("unknown runtime error = %v", err)
	}
}

func TestGatewayControlStorePreservesAdministrativeDrainOnReconnect(t *testing.T) {
	for _, desired := range []string{"drain"} {
		t.Run(desired, func(t *testing.T) {
			repo := &fakeGatewayControlRepo{accepted: domain.GatewayAcceptedConnection{ConnectionEpoch: 11, DesiredLifecycle: desired}}
			connection, err := (gatewayControlStore{repo: repo}).Accept(context.Background(), "gw_1", apigateway.Hello{
				RuntimeState: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY,
			})
			if err != nil {
				t.Fatal(err)
			}
			if connection.DesiredLifecycle != gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN {
				t.Fatalf("desired lifecycle = %v", connection.DesiredLifecycle)
			}
		})
	}
	repo := &fakeGatewayControlRepo{err: domain.ErrConflict("connection superseded")}
	if _, err := (gatewayControlStore{repo: repo}).Accept(context.Background(), "gw_1", apigateway.Hello{
		RuntimeState: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY,
	}); !errors.Is(err, apigateway.ErrConflict) {
		t.Fatalf("superseded accept error = %v", err)
	}
}

func TestGatewayControlStoreObservedShutdownDoesNotLatchNextRestartToDrain(t *testing.T) {
	repo := &fakeGatewayControlRepo{
		accepted:    domain.GatewayAcceptedConnection{ConnectionEpoch: 12, DesiredLifecycle: "run"},
		lifecycleOK: true,
	}
	controlStore := gatewayControlStore{repo: repo}
	if err := controlStore.Lifecycle(context.Background(), "gw_1", 11, apigateway.LifecycleReport{
		State: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED,
	}); err != nil {
		t.Fatal(err)
	}
	connection, err := controlStore.Accept(context.Background(), "gw_1", apigateway.Hello{
		RuntimeState: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_STARTING,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.lifecycle.Status != domain.GatewayDrained || connection.DesiredLifecycle != gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN {
		t.Fatalf("observed=%q desired=%v", repo.lifecycle.Status, connection.DesiredLifecycle)
	}
}

func TestGatewayControlStorePreservesContextErrors(t *testing.T) {
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(contextErr.Error(), func(t *testing.T) {
			repo := &fakeGatewayControlRepo{err: contextErr}
			controlStore := gatewayControlStore{repo: repo}
			_, acceptErr := controlStore.Accept(context.Background(), "gw_1", apigateway.Hello{RuntimeState: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY})
			if !errors.Is(acceptErr, contextErr) || !errors.Is(acceptErr, apigateway.ErrUnavailable) {
				t.Fatalf("accept error = %v", acceptErr)
			}
			heartbeatErr := controlStore.Heartbeat(context.Background(), "gw_1", 1, apigateway.Heartbeat{RuntimeState: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY})
			if !errors.Is(heartbeatErr, contextErr) || !errors.Is(heartbeatErr, apigateway.ErrUnavailable) {
				t.Fatalf("heartbeat error = %v", heartbeatErr)
			}
		})
	}
}

func TestPrivateServerRegistersGatewayControl(t *testing.T) {
	control := &apigateway.Server{}
	server := newPrivateGatewayGRPCServer(&tls.Config{}, privateGatewayAuthenticator{}, &fakeRedeemer{}, nil, control)
	if _, ok := server.GetServiceInfo()[gatewayv1.GatewayControlService_ServiceDesc.ServiceName]; !ok {
		t.Fatal("gateway control absent")
	}
}

func TestPrivateTLSCompositionIsPinnedTLS13AndOptionalClientCert(t *testing.T) {
	roots := x509.NewCertPool()
	config := privateGatewayTLSConfig((*apiidentity.Manager)(nil), roots)
	if config.MinVersion != tls.VersionTLS13 || config.ClientAuth != tls.VerifyClientCertIfGiven || config.ClientCAs != roots || config.GetCertificate == nil {
		t.Fatalf("TLS configuration = %+v", config)
	}
}
