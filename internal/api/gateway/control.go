// Package gateway implements the API-owned gateway control-plane application
// service. Persistence is deliberately expressed in transport-neutral values.
package gateway

import (
	"context"
	"errors"
	"io"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const ProtocolVersion uint32 = 1

const (
	MaxInstanceIDBytes      = 128
	MaxSoftwareVersionBytes = 128
	MaxGRPCEndpointBytes    = 512
	MaxGatewayCapabilities  = 16
	maxUnixMillis           = int64(253402300799999)
)

var (
	ErrUnauthorized = errors.New("gateway unauthorized")
	ErrConflict     = errors.New("gateway connection conflict")
	ErrStaleEpoch   = errors.New("stale gateway connection epoch")
	ErrUnavailable  = errors.New("gateway persistence unavailable")
)

type Hello struct {
	InstanceID, SoftwareVersion, GRPCEndpoint string
	Capabilities                              []gatewayv1.GatewayCapability
	StartedAt                                 time.Time
	SessionCount                              uint32
	RuntimeState                              gatewayv1.GatewayRuntimeState
}

type Heartbeat struct {
	LastControlSequence uint64
	SentAt              time.Time
	SessionCount        uint32
	RuntimeState        gatewayv1.GatewayRuntimeState
}

type LifecycleReport struct {
	DirectiveID string
	State       gatewayv1.GatewayRuntimeState
	Failure     gatewayv1.LifecycleFailure
}

type Connection struct {
	ID                string
	Epoch             uint64
	HeartbeatInterval time.Duration
	LeaseTimeout      time.Duration
	DesiredLifecycle  gatewayv1.LifecycleDirectiveAction
}

// Store owns atomic connection acceptance (including epoch allocation and hello
// metadata persistence) and fences every later mutation by gateway ID and epoch.
type Store interface {
	Accept(context.Context, string, Hello) (Connection, error)
	Heartbeat(context.Context, string, uint64, Heartbeat) error
	Lifecycle(context.Context, string, uint64, LifecycleReport) error
	Disconnect(context.Context, string, uint64) error
}

type Server struct {
	gatewayv1.UnimplementedGatewayControlServiceServer
	Store Store
	// ResolveGatewayID must read the identity installed by the private listener's
	// mTLS authenticator. It must never inspect a control-frame payload.
	ResolveGatewayID func(context.Context) (string, bool)
	Now              func() time.Time
}

func (s *Server) Connect(stream gatewayv1.GatewayControlService_ConnectServer) (result error) {
	if s.ResolveGatewayID == nil {
		return status.Error(codes.Unauthenticated, "gateway authentication required")
	}
	gatewayID, authenticated := s.ResolveGatewayID(stream.Context())
	if !authenticated || gatewayID == "" {
		return status.Error(codes.Unauthenticated, "gateway authentication required")
	}
	if s.Store == nil {
		return status.Error(codes.Unavailable, "gateway control unavailable")
	}

	first, err := stream.Recv()
	if err != nil {
		return receiveStatus(err)
	}
	if err = validateEnvelope(first, 1); err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.FailedPrecondition, "hello must be the first gateway frame")
	}
	if err = validateHello(hello); err != nil {
		return err
	}
	connection, err := s.Store.Accept(stream.Context(), gatewayID, helloValue(hello))
	if err != nil {
		return storeStatus(err)
	}
	if connection.ID == "" || connection.Epoch == 0 || connection.HeartbeatInterval <= 0 || connection.LeaseTimeout <= 0 {
		return status.Error(codes.Internal, "invalid gateway connection allocation")
	}
	defer func() {
		ctx := context.WithoutCancel(stream.Context())
		_ = s.Store.Disconnect(ctx, gatewayID, connection.Epoch)
	}()

	welcome := &gatewayv1.ControlFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        1,
		Payload: &gatewayv1.ControlFrame_Welcome{Welcome: &gatewayv1.ControlWelcome{
			ConnectionId:        connection.ID,
			ConnectionEpoch:     connection.Epoch,
			HeartbeatIntervalMs: durationMillis(connection.HeartbeatInterval),
			LeaseTimeoutMs:      durationMillis(connection.LeaseTimeout),
			DesiredLifecycle:    connection.DesiredLifecycle,
			ServerTimeUnixMs:    s.clock().UnixMilli(),
		}},
	}
	if err = stream.Send(welcome); err != nil {
		return sendStatus(err)
	}

	for expected := uint64(2); ; expected++ {
		frame, recvErr := stream.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				return nil
			}
			return receiveStatus(recvErr)
		}
		if err = validateEnvelope(frame, expected); err != nil {
			return err
		}
		switch payload := frame.Payload.(type) {
		case *gatewayv1.GatewayFrame_Heartbeat:
			if err = validateHeartbeat(payload.Heartbeat); err != nil {
				return err
			}
			if payload.Heartbeat.ConnectionEpoch != connection.Epoch {
				return status.Error(codes.FailedPrecondition, "gateway connection epoch mismatch")
			}
			if payload.Heartbeat.LastControlSequence != welcome.Sequence {
				return status.Error(codes.FailedPrecondition, "gateway control acknowledgement mismatch")
			}
			err = s.Store.Heartbeat(stream.Context(), gatewayID, connection.Epoch, heartbeatValue(payload.Heartbeat))
		case *gatewayv1.GatewayFrame_LifecycleReport:
			return status.Error(codes.FailedPrecondition, "no lifecycle directive is awaiting acknowledgement")
		default:
			return status.Error(codes.FailedPrecondition, "hello is only valid as the first gateway frame")
		}
		if err != nil {
			return storeStatus(err)
		}
	}
}

func validateHello(hello *gatewayv1.GatewayHello) error {
	if hello == nil {
		return status.Error(codes.InvalidArgument, "gateway hello required")
	}
	if len(hello.InstanceId) == 0 || len(hello.InstanceId) > MaxInstanceIDBytes {
		return status.Error(codes.InvalidArgument, "invalid gateway instance id")
	}
	if len(hello.SoftwareVersion) == 0 || len(hello.SoftwareVersion) > MaxSoftwareVersionBytes {
		return status.Error(codes.InvalidArgument, "invalid gateway software version")
	}
	if hello.GrpcEndpoint != nil && len(*hello.GrpcEndpoint) > MaxGRPCEndpointBytes {
		return status.Error(codes.InvalidArgument, "invalid gateway gRPC endpoint")
	}
	if hello.StartedAtUnixMs <= 0 || hello.StartedAtUnixMs > maxUnixMillis {
		return status.Error(codes.InvalidArgument, "invalid gateway start time")
	}
	if !knownRuntimeState(hello.RuntimeState) {
		return status.Error(codes.InvalidArgument, "invalid gateway runtime state")
	}
	if len(hello.Capabilities) > MaxGatewayCapabilities {
		return status.Error(codes.InvalidArgument, "too many gateway capabilities")
	}
	seen := make(map[gatewayv1.GatewayCapability]struct{}, len(hello.Capabilities))
	for _, capability := range hello.Capabilities {
		if !knownCapability(capability) {
			return status.Error(codes.InvalidArgument, "invalid gateway capability")
		}
		if _, duplicate := seen[capability]; duplicate {
			return status.Error(codes.InvalidArgument, "duplicate gateway capability")
		}
		seen[capability] = struct{}{}
	}
	return nil
}

func validateHeartbeat(heartbeat *gatewayv1.GatewayHeartbeat) error {
	if heartbeat == nil || heartbeat.ConnectionEpoch == 0 || heartbeat.SentAtUnixMs <= 0 || heartbeat.SentAtUnixMs > maxUnixMillis || !knownRuntimeState(heartbeat.RuntimeState) {
		return status.Error(codes.InvalidArgument, "invalid gateway heartbeat")
	}
	return nil
}

func knownRuntimeState(state gatewayv1.GatewayRuntimeState) bool {
	switch state {
	case gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_STARTING,
		gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY,
		gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING,
		gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED,
		gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED:
		return true
	default:
		return false
	}
}

func knownCapability(capability gatewayv1.GatewayCapability) bool {
	switch capability {
	case gatewayv1.GatewayCapability_GATEWAY_CAPABILITY_SESSION_ENGINE,
		gatewayv1.GatewayCapability_GATEWAY_CAPABILITY_EVENT_STREAM,
		gatewayv1.GatewayCapability_GATEWAY_CAPABILITY_COMMAND_EXECUTION:
		return true
	default:
		return false
	}
}

func validateEnvelope(frame *gatewayv1.GatewayFrame, sequence uint64) error {
	if frame == nil || frame.ProtocolVersion != ProtocolVersion {
		return status.Error(codes.FailedPrecondition, "unsupported gateway control protocol")
	}
	if frame.Sequence != sequence {
		return status.Error(codes.FailedPrecondition, "gateway frame sequence mismatch")
	}
	if frame.Payload == nil {
		return status.Error(codes.InvalidArgument, "gateway frame payload required")
	}
	return nil
}

func helloValue(v *gatewayv1.GatewayHello) Hello {
	endpoint := ""
	if v.GrpcEndpoint != nil {
		endpoint = *v.GrpcEndpoint
	}
	return Hello{InstanceID: v.InstanceId, SoftwareVersion: v.SoftwareVersion, GRPCEndpoint: endpoint, Capabilities: append([]gatewayv1.GatewayCapability(nil), v.Capabilities...), StartedAt: time.UnixMilli(v.StartedAtUnixMs).UTC(), SessionCount: v.SessionCount, RuntimeState: v.RuntimeState}
}

func heartbeatValue(v *gatewayv1.GatewayHeartbeat) Heartbeat {
	return Heartbeat{LastControlSequence: v.LastControlSequence, SentAt: time.UnixMilli(v.SentAtUnixMs).UTC(), SessionCount: v.SessionCount, RuntimeState: v.RuntimeState}
}

func lifecycleValue(v *gatewayv1.GatewayLifecycleReport) LifecycleReport {
	return LifecycleReport{DirectiveID: v.DirectiveId, State: v.State, Failure: v.Failure}
}

func durationMillis(v time.Duration) uint32 {
	ms := v / time.Millisecond
	if ms > time.Duration(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(ms)
}

func (s *Server) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func storeStatus(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "gateway control canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "gateway control deadline exceeded")
	case errors.Is(err, ErrUnauthorized):
		return status.Error(codes.PermissionDenied, "gateway is not authorized")
	case errors.Is(err, ErrConflict):
		return status.Error(codes.Aborted, "gateway connection conflict")
	case errors.Is(err, ErrStaleEpoch):
		return status.Error(codes.FailedPrecondition, "stale gateway connection epoch")
	case errors.Is(err, ErrUnavailable):
		return status.Error(codes.Unavailable, "gateway control temporarily unavailable")
	default:
		return status.Error(codes.Internal, "gateway control failed")
	}
}

func receiveStatus(err error) error {
	if errors.Is(err, io.EOF) {
		return status.Error(codes.FailedPrecondition, "hello frame required")
	}
	return sendStatus(err)
}

func sendStatus(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "gateway control canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "gateway control deadline exceeded")
	default:
		return status.Error(codes.Unavailable, "gateway control stream unavailable")
	}
}
