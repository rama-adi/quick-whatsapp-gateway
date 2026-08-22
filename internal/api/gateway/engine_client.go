package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// EngineTargetResolver is the only route authority accepted by Client.
type EngineTargetResolver interface {
	ResolveSessionEngineTarget(context.Context, string, string) (domain.SessionEngineTarget, error)
}
type EngineDial func(context.Context, string, string) (*grpc.ClientConn, error)

// MaxEngineMessageBytes bounds one engine RPC message. The album aggregate cap
// (64 MiB) plus envelope/JSON headroom decides the value; both dial and server
// must agree so inline media payloads are never silently truncated.
const MaxEngineMessageBytes = 68 << 20

// NewEngineMTLSDial constructs a gateway-bound TLS 1.3 dialer. The callback is
// supplied by apiidentity.Manager so rotations affect new pooled connections.
func NewEngineMTLSDial(certificate func(*tls.ClientHelloInfo) (*tls.Certificate, error), roots *x509.CertPool) EngineDial {
	return func(ctx context.Context, gatewayID, endpoint string) (*grpc.ClientConn, error) {
		if certificate == nil || roots == nil {
			return nil, errors.New("gateway engine mTLS is not configured")
		}
		return grpc.DialContext(ctx, endpoint, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return certificate(nil) }, VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("gateway certificate missing")
			}
			leaf := state.PeerCertificates[0]
			inter := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] {
				inter.AddCert(cert)
			}
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
				return err
			}
			if len(leaf.URIs) != 1 || leaf.URIs[0].String() != "spiffe://quick-wa/gateway/"+gatewayID {
				return errors.New("gateway SPIFFE identity mismatch")
			}
			return nil
		}})), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxEngineMessageBytes), grpc.MaxCallSendMsgSize(MaxEngineMessageBytes)))
	}
}

// EngineClient implements the API-facing resolved live facade. Calls reuse one
// grpc-go ClientConn per advertised endpoint; grpc-go owns reconnects.
type EngineClient struct {
	resolver     EngineTargetResolver
	dial         EngineDial
	mu           sync.Mutex
	conns        map[string]*grpc.ClientConn
	deadline     time.Duration
	sendDeadline time.Duration
	health       map[string]EngineHealth
}
type EngineHealth struct {
	GatewayID, Endpoint      string
	LastAttempt, LastSuccess time.Time
	LastError                string
}

func NewEngineClient(resolver EngineTargetResolver, dial EngineDial, deadline, sendDeadline time.Duration) (*EngineClient, error) {
	if deadline <= 0 {
		return nil, errors.New("gateway engine unary deadline is required")
	}
	if sendDeadline <= 0 {
		return nil, errors.New("gateway engine send deadline is required")
	}
	return &EngineClient{resolver: resolver, dial: dial, conns: map[string]*grpc.ClientConn{}, deadline: deadline, sendDeadline: sendDeadline, health: map[string]EngineHealth{}}, nil
}
func (c *EngineClient) Health() []EngineHealth {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]EngineHealth, 0, len(c.health))
	for _, v := range c.health {
		out = append(out, v)
	}
	return out
}
func (c *EngineClient) record(gatewayID, endpoint string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := gatewayID + "\x00" + endpoint
	value := c.health[key]
	value.GatewayID = gatewayID
	value.Endpoint = endpoint
	value.LastAttempt = time.Now().UTC()
	if err == nil {
		value.LastSuccess = value.LastAttempt
		value.LastError = ""
	} else {
		value.LastError = err.Error()
	}
	c.health[key] = value
}
func (c *EngineClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var first error
	for k, conn := range c.conns {
		if err := conn.Close(); err != nil && first == nil {
			first = err
		}
		delete(c.conns, k)
	}
	return first
}
func (c *EngineClient) conn(ctx context.Context, gatewayID, endpoint string) (*grpc.ClientConn, error) {
	key := gatewayID + "\x00" + endpoint
	c.mu.Lock()
	if conn := c.conns[key]; conn != nil {
		c.mu.Unlock()
		return conn, nil
	}
	c.mu.Unlock()
	conn, err := c.dial(ctx, gatewayID, endpoint)
	c.record(gatewayID, endpoint, err)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if old := c.conns[key]; old != nil {
		c.mu.Unlock()
		_ = conn.Close()
		return old, nil
	}
	c.conns[key] = conn
	c.mu.Unlock()
	return conn, nil
}
func (c *EngineClient) GetSessionState(ctx context.Context, org, session string) (application.SessionState, error) {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return application.SessionState{}, err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return application.SessionState{}, err
	}
	response, err := gatewayv1.NewGatewayEngineServiceClient(conn).GetSessionState(ctx, &gatewayv1.GetSessionStateRequest{Target: &gatewayv1.SessionTarget{OrganizationId: target.OrganizationID, SessionId: target.SessionID, GatewayId: target.GatewayID}, AssignmentEpoch: target.AssignmentEpoch})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	if err != nil {
		return application.SessionState{}, mapEngineError(err)
	}
	state, err := sessionStatus(response.Status)
	if err != nil {
		return application.SessionState{}, err
	}
	return application.SessionState{OrganizationID: target.OrganizationID, SessionID: target.SessionID, GatewayID: target.GatewayID, Status: state, Connected: response.Connected, LoggedIn: response.LoggedIn}, nil
}
func (c *EngineClient) SetAccountPresence(ctx context.Context, org, session string, presence application.AccountPresence) error {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return err
	}
	state := gatewayv1.AccountPresence_ACCOUNT_PRESENCE_ONLINE
	if presence == application.AccountPresenceOffline {
		state = gatewayv1.AccountPresence_ACCOUNT_PRESENCE_OFFLINE
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return err
	}
	_, err = gatewayv1.NewGatewayEngineServiceClient(conn).SetAccountPresence(ctx, &gatewayv1.SetAccountPresenceRequest{Target: &gatewayv1.SessionTarget{OrganizationId: target.OrganizationID, SessionId: target.SessionID, GatewayId: target.GatewayID}, AssignmentEpoch: target.AssignmentEpoch, CommandId: domain.NewULID(), State: state})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	return mapEngineError(err)
}

// SendMessage dispatches one durable send command. The caller owns the stable
// CommandID (from the durable command row); this method never mints one, so a
// retried ambiguous send re-issues the same id and the gateway's ledger
// returns the original terminal result.
func (c *EngineClient) SendMessage(ctx context.Context, command application.SendCommand) (application.SendMessageResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.sendDeadline)
	defer cancel()
	if command.CommandID == "" {
		return application.SendMessageResult{}, domain.ErrValidation("send command id is required")
	}
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, command.OrganizationID, command.SessionID)
	if err != nil {
		return application.SendMessageResult{}, err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return application.SendMessageResult{}, err
	}
	payload, err := json.Marshal(command.Payload)
	if err != nil {
		return application.SendMessageResult{}, fmt.Errorf("encode send payload: %w", err)
	}
	response, err := gatewayv1.NewGatewayEngineServiceClient(conn).SendMessage(ctx, &gatewayv1.SendMessageRequest{
		Target:          &gatewayv1.SessionTarget{OrganizationId: target.OrganizationID, SessionId: target.SessionID, GatewayId: target.GatewayID},
		AssignmentEpoch: target.AssignmentEpoch,
		CommandId:       command.CommandID,
		PayloadJson:     payload,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	if err != nil {
		return application.SendMessageResult{}, mapEngineError(err)
	}
	return application.SendMessageResult{
		MutationResult: application.MutationResult{
			CommandID: response.CommandId, OrganizationID: target.OrganizationID,
			SessionID: target.SessionID, GatewayID: target.GatewayID, AssignmentEpoch: target.AssignmentEpoch,
		},
		WAMessageID: response.WaMessageId,
		SentAt:      time.UnixMilli(response.SentAtUnixMs).UTC(),
	}, nil
}
func mapEngineError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.NotFound:
		return domain.ErrNotFound("gateway session not found")
	case codes.FailedPrecondition:
		return domain.ErrConflict("gateway assignment changed")
	case codes.Unavailable:
		return domain.ErrUnavailable("gateway unavailable")
	case codes.InvalidArgument:
		return domain.ErrValidation("gateway rejected request")
	default:
		return fmt.Errorf("gateway engine: %w", err)
	}
}

func sessionStatus(value gatewayv1.GatewaySessionStatus) (domain.SessionStatus, error) {
	switch value {
	case gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_STOPPED:
		return domain.SessionStopped, nil
	case gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_STARTING:
		return domain.SessionStarting, nil
	case gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_SCAN_QR:
		return domain.SessionScanQR, nil
	case gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_WORKING:
		return domain.SessionWorking, nil
	case gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_LOGGED_OUT:
		return domain.SessionLoggedOut, nil
	default:
		return "", domain.ErrValidation("gateway returned unknown session status")
	}
}
