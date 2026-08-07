// Package gateway implements the API-owned gateway control-plane application
// service. Persistence is deliberately expressed in transport-neutral values.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const ProtocolVersion uint32 = 1

const (
	DefaultHelloTimeout      = 10 * time.Second
	DefaultHeartbeatInterval = 5 * time.Second
	DefaultLeaseTimeout      = 15 * time.Second
	DefaultDisconnectTimeout = 5 * time.Second
	MaxInstanceIDBytes       = 128
	MaxSoftwareVersionBytes  = 128
	MaxGRPCEndpointBytes     = 512
	MaxHTTPBaseURLBytes      = 512
	MaxGatewayCapabilities   = 16
	maxUnixMillis            = int64(253402300799999)
)

var (
	ErrUnauthorized = errors.New("gateway unauthorized")
	ErrConflict     = errors.New("gateway connection conflict")
	ErrStaleEpoch   = errors.New("stale gateway connection epoch")
	ErrUnavailable  = errors.New("gateway persistence unavailable")
)

type Hello struct {
	InstanceID, SoftwareVersion, GRPCEndpoint, HTTPBaseURL string
	Capabilities                                           []gatewayv1.GatewayCapability
	StartedAt                                              time.Time
	SessionCount                                           uint32
	RuntimeState                                           gatewayv1.GatewayRuntimeState
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

// DesiredLifecycle is the authoritative desired state observed as part of a
// successful fenced heartbeat. Revision identifies one desired-state command;
// the handler emits at most one directive for each revision on a connection.
type DesiredLifecycle struct {
	Action   gatewayv1.LifecycleDirectiveAction
	Revision uint64
}

type Connection struct {
	ID                string
	Epoch             uint64
	HeartbeatInterval time.Duration
	LeaseTimeout      time.Duration
	DesiredLifecycle  gatewayv1.LifecycleDirectiveAction
	DesiredRevision   uint64
}

// Store owns atomic connection acceptance (including epoch allocation and hello
// metadata persistence) and fences every later mutation by gateway ID and epoch.
type Store interface {
	Accept(context.Context, string, Hello) (Connection, error)
	// Heartbeat durably records the report for the current epoch and returns
	// the desired lifecycle/revision read from that same fenced connection.
	Heartbeat(context.Context, string, uint64, Heartbeat) (DesiredLifecycle, error)
	Lifecycle(context.Context, string, uint64, LifecycleReport) error
	Disconnect(context.Context, string, uint64) error
}

type Server struct {
	gatewayv1.UnimplementedGatewayControlServiceServer
	Store Store
	// ResolveGatewayID must read the identity installed by the private listener's
	// mTLS authenticator. It must never inspect a control-frame payload.
	ResolveGatewayID  func(context.Context) (string, bool)
	Now               func() time.Time
	HelloTimeout      time.Duration
	DisconnectTimeout time.Duration
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

	received := receiveFrames(stream)
	helloTimer := time.NewTimer(s.helloTimeout())
	defer helloTimer.Stop()
	first, err := receiveBefore(stream.Context(), received, helloTimer.C, "gateway hello deadline exceeded")
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
	if connection.ID == "" || connection.Epoch == 0 || connection.HeartbeatInterval <= 0 || connection.LeaseTimeout <= 0 || !knownLifecycleAction(connection.DesiredLifecycle) {
		return status.Error(codes.Internal, "invalid gateway connection allocation")
	}
	defer func() {
		ctx, cancel := s.disconnectContext(stream.Context())
		defer cancel()
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

	leaseTimer := time.NewTimer(connection.LeaseTimeout)
	defer leaseTimer.Stop()
	outboundSequence := welcome.Sequence
	lastAcknowledgedControlSequence := welcome.Sequence
	lastIssuedDesiredRevision := connection.DesiredRevision
	lastIssuedDesiredAction := connection.DesiredLifecycle
	var pendingDirective *issuedDirective
	for expected := uint64(2); ; expected++ {
		frame, recvErr := receiveBefore(stream.Context(), received, leaseTimer.C, "gateway heartbeat lease expired")
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				return nil
			}
			return recvErr
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
			if payload.Heartbeat.LastControlSequence > outboundSequence || payload.Heartbeat.LastControlSequence < lastAcknowledgedControlSequence {
				return status.Error(codes.FailedPrecondition, "gateway control acknowledgement mismatch")
			}
			lastAcknowledgedControlSequence = payload.Heartbeat.LastControlSequence
			desired, heartbeatErr := s.Store.Heartbeat(stream.Context(), gatewayID, connection.Epoch, heartbeatValue(payload.Heartbeat))
			if heartbeatErr == nil {
				if err = validateDesiredLifecycle(desired); err != nil {
					return err
				}
				if desired.Revision < lastIssuedDesiredRevision || (desired.Revision == lastIssuedDesiredRevision && desired.Action != lastIssuedDesiredAction) {
					return status.Error(codes.Internal, "invalid desired lifecycle revision")
				}
				resetTimer(leaseTimer, connection.LeaseTimeout)
				outboundSequence++
				if err = stream.Send(&gatewayv1.ControlFrame{
					ProtocolVersion: ProtocolVersion,
					Sequence:        outboundSequence,
					Payload: &gatewayv1.ControlFrame_HeartbeatAck{HeartbeatAck: &gatewayv1.ControlHeartbeatAck{
						AcknowledgedGatewaySequence: frame.Sequence,
						ConnectionEpoch:             connection.Epoch,
						ServerTimeUnixMs:            s.clock().UnixMilli(),
					}},
				}); err != nil {
					return sendStatus(err)
				}
				if pendingDirective == nil && desired.Revision != lastIssuedDesiredRevision {
					directive := issuedDirective{id: directiveID(connection.ID, desired.Revision), action: desired.Action}
					outboundSequence++
					if err = stream.Send(&gatewayv1.ControlFrame{
						ProtocolVersion: ProtocolVersion,
						Sequence:        outboundSequence,
						Payload: &gatewayv1.ControlFrame_LifecycleDirective{LifecycleDirective: &gatewayv1.LifecycleDirective{
							DirectiveId:     directive.id,
							ConnectionEpoch: connection.Epoch,
							Action:          directive.action,
							Reason:          gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_OPERATOR,
						}},
					}); err != nil {
						return sendStatus(err)
					}
					pendingDirective = &directive
					lastIssuedDesiredRevision = desired.Revision
					lastIssuedDesiredAction = desired.Action
				}
			} else {
				err = heartbeatErr
			}
		case *gatewayv1.GatewayFrame_LifecycleReport:
			if pendingDirective == nil {
				return status.Error(codes.FailedPrecondition, "no lifecycle directive is awaiting acknowledgement")
			}
			if err = validateLifecycleReport(payload.LifecycleReport, connection.Epoch, *pendingDirective); err != nil {
				return err
			}
			err = s.Store.Lifecycle(stream.Context(), gatewayID, connection.Epoch, lifecycleValue(payload.LifecycleReport))
			if err == nil {
				pendingDirective = nil
			}
		default:
			return status.Error(codes.FailedPrecondition, "hello is only valid as the first gateway frame")
		}
		if err != nil {
			return storeStatus(err)
		}
	}
}

type receiveResult struct {
	frame *gatewayv1.GatewayFrame
	err   error
}

func receiveFrames(stream gatewayv1.GatewayControlService_ConnectServer) <-chan receiveResult {
	results := make(chan receiveResult, 1)
	go func() {
		defer close(results)
		for {
			frame, err := stream.Recv()
			select {
			case results <- receiveResult{frame: frame, err: err}:
			case <-stream.Context().Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return results
}

func receiveBefore(ctx context.Context, received <-chan receiveResult, deadline <-chan time.Time, timeoutMessage string) (*gatewayv1.GatewayFrame, error) {
	select {
	case <-ctx.Done():
		return nil, receiveStatus(ctx.Err())
	case <-deadline:
		return nil, status.Error(codes.DeadlineExceeded, timeoutMessage)
	case result, ok := <-received:
		if !ok {
			return nil, status.Error(codes.Unavailable, "gateway control stream unavailable")
		}
		if result.err != nil {
			return nil, result.err
		}
		return result.frame, nil
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
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
	if hello.HttpBaseUrl != nil {
		if err := validateHTTPBaseURL(*hello.HttpBaseUrl); err != nil {
			return err
		}
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

type issuedDirective struct {
	id     string
	action gatewayv1.LifecycleDirectiveAction
}

func directiveID(connectionID string, revision uint64) string {
	return fmt.Sprintf("%s:%d", connectionID, revision)
}

func validateDesiredLifecycle(desired DesiredLifecycle) error {
	if !knownLifecycleAction(desired.Action) {
		return status.Error(codes.Internal, "invalid desired lifecycle")
	}
	return nil
}

func validateLifecycleReport(report *gatewayv1.GatewayLifecycleReport, epoch uint64, directive issuedDirective) error {
	if report == nil || report.DirectiveId == "" || !knownRuntimeState(report.State) || !knownLifecycleFailure(report.Failure) {
		return status.Error(codes.InvalidArgument, "invalid gateway lifecycle report")
	}
	if report.ConnectionEpoch != epoch {
		return status.Error(codes.FailedPrecondition, "gateway connection epoch mismatch")
	}
	if report.DirectiveId != directive.id {
		return status.Error(codes.FailedPrecondition, "gateway lifecycle directive acknowledgement mismatch")
	}
	if report.Failure == gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE && !lifecycleActionSatisfied(directive.action, report.State) {
		return status.Error(codes.FailedPrecondition, "gateway lifecycle directive result mismatch")
	}
	return nil
}

func knownLifecycleAction(action gatewayv1.LifecycleDirectiveAction) bool {
	switch action {
	case gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN,
		gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN,
		gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DISABLE:
		return true
	default:
		return false
	}
}

func knownLifecycleFailure(failure gatewayv1.LifecycleFailure) bool {
	switch failure {
	case gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE,
		gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_BUSY,
		gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_TIMEOUT,
		gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_INTERNAL:
		return true
	default:
		return false
	}
}

func lifecycleActionSatisfied(action gatewayv1.LifecycleDirectiveAction, state gatewayv1.GatewayRuntimeState) bool {
	switch action {
	case gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN:
		return state == gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY
	case gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN,
		gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DISABLE:
		return state == gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED
	default:
		return false
	}
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
	httpBaseURL := ""
	if v.HttpBaseUrl != nil {
		httpBaseURL = *v.HttpBaseUrl
	}
	return Hello{InstanceID: v.InstanceId, SoftwareVersion: v.SoftwareVersion, GRPCEndpoint: endpoint, HTTPBaseURL: httpBaseURL, Capabilities: append([]gatewayv1.GatewayCapability(nil), v.Capabilities...), StartedAt: time.UnixMilli(v.StartedAtUnixMs).UTC(), SessionCount: v.SessionCount, RuntimeState: v.RuntimeState}
}

func validateHTTPBaseURL(raw string) error {
	if len(raw) == 0 || len(raw) > MaxHTTPBaseURLBytes {
		return status.Error(codes.InvalidArgument, "invalid gateway HTTP base URL")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Path != "" {
		return status.Error(codes.InvalidArgument, "invalid gateway HTTP base URL")
	}
	if u.String() != raw {
		return status.Error(codes.InvalidArgument, "invalid gateway HTTP base URL")
	}
	return nil
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

func (s *Server) helloTimeout() time.Duration {
	if s.HelloTimeout > 0 {
		return s.HelloTimeout
	}
	return DefaultHelloTimeout
}

func (s *Server) disconnectTimeout() time.Duration {
	if s.DisconnectTimeout > 0 {
		return s.DisconnectTimeout
	}
	return DefaultDisconnectTimeout
}

func (s *Server) disconnectContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), s.disconnectTimeout())
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
	if status.Code(err) != codes.Unknown {
		return err
	}
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
