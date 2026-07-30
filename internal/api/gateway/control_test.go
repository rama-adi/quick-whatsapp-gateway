package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeStore struct {
	connection Connection
	acceptErr  error
	writeErr   error
	acceptedID string
	heartbeats []uint64
	lifecycles []uint64
	disconnect []uint64
}

func (s *fakeStore) Accept(_ context.Context, id string, _ Hello) (Connection, error) {
	s.acceptedID = id
	return s.connection, s.acceptErr
}
func (s *fakeStore) Heartbeat(_ context.Context, _ string, epoch uint64, _ Heartbeat) error {
	s.heartbeats = append(s.heartbeats, epoch)
	return s.writeErr
}
func (s *fakeStore) Lifecycle(_ context.Context, _ string, epoch uint64, _ LifecycleReport) error {
	s.lifecycles = append(s.lifecycles, epoch)
	return s.writeErr
}
func (s *fakeStore) Disconnect(_ context.Context, _ string, epoch uint64) error {
	s.disconnect = append(s.disconnect, epoch)
	return nil
}

type fakeStream struct {
	ctx    context.Context
	frames []*gatewayv1.GatewayFrame
	sent   []*gatewayv1.ControlFrame
}

func (s *fakeStream) Context() context.Context { return s.ctx }
func (s *fakeStream) Recv() (*gatewayv1.GatewayFrame, error) {
	if len(s.frames) == 0 {
		return nil, io.EOF
	}
	frame := s.frames[0]
	s.frames = s.frames[1:]
	return frame, nil
}
func (s *fakeStream) Send(frame *gatewayv1.ControlFrame) error {
	s.sent = append(s.sent, frame)
	return nil
}
func (*fakeStream) SetHeader(metadata.MD) error  { return nil }
func (*fakeStream) SendHeader(metadata.MD) error { return nil }
func (*fakeStream) SetTrailer(metadata.MD)       {}
func (*fakeStream) SendMsg(any) error            { return nil }
func (*fakeStream) RecvMsg(any) error            { return nil }

func hello(sequence uint64) *gatewayv1.GatewayFrame {
	return &gatewayv1.GatewayFrame{ProtocolVersion: ProtocolVersion, Sequence: sequence, Payload: &gatewayv1.GatewayFrame_Hello{Hello: &gatewayv1.GatewayHello{
		InstanceId: "process-1", SoftwareVersion: "v2.0.0", StartedAtUnixMs: 1,
		RuntimeState: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY,
		Capabilities: []gatewayv1.GatewayCapability{gatewayv1.GatewayCapability_GATEWAY_CAPABILITY_SESSION_ENGINE},
	}}}
}

func heartbeat(sequence, epoch, ack uint64) *gatewayv1.GatewayFrame {
	return &gatewayv1.GatewayFrame{ProtocolVersion: ProtocolVersion, Sequence: sequence, Payload: &gatewayv1.GatewayFrame_Heartbeat{Heartbeat: &gatewayv1.GatewayHeartbeat{
		ConnectionEpoch: epoch, LastControlSequence: ack, SentAtUnixMs: 2,
		RuntimeState: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY,
	}}}
}

func testServer(store *fakeStore) *Server {
	return &Server{Store: store, ResolveGatewayID: func(context.Context) (string, bool) { return "gw_cert", true }, Now: func() time.Time { return time.UnixMilli(1234) }}
}

func connectedStore() *fakeStore {
	return &fakeStore{connection: Connection{ID: "conn-1", Epoch: 7, HeartbeatInterval: 5 * time.Second, LeaseTimeout: 15 * time.Second, DesiredLifecycle: gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN}}
}

func TestConnectUsesTLSIdentityAndSendsSequencedWelcome(t *testing.T) {
	store := connectedStore()
	stream := &fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{hello(1), heartbeat(2, 7, 1)}}
	if err := testServer(store).Connect(stream); err != nil {
		t.Fatal(err)
	}
	if store.acceptedID != "gw_cert" || len(store.heartbeats) != 1 || store.heartbeats[0] != 7 {
		t.Fatalf("store calls = id %q heartbeats %v", store.acceptedID, store.heartbeats)
	}
	if len(stream.sent) != 1 || stream.sent[0].Sequence != 1 || stream.sent[0].ProtocolVersion != ProtocolVersion || stream.sent[0].GetWelcome().ConnectionEpoch != 7 {
		t.Fatalf("welcome = %+v", stream.sent)
	}
	if len(store.disconnect) != 1 || store.disconnect[0] != 7 {
		t.Fatalf("disconnects = %v", store.disconnect)
	}
}

func TestConnectRequiresAuthenticatedTLSPeer(t *testing.T) {
	server := testServer(connectedStore())
	server.ResolveGatewayID = func(context.Context) (string, bool) { return "", false }
	err := server.Connect(&fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{hello(1)}})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, err = %v", status.Code(err), err)
	}
}

func TestConnectRequiresHelloFirst(t *testing.T) {
	stream := &fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{heartbeat(1, 1, 0)}}
	err := testServer(connectedStore()).Connect(stream)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v, err = %v", status.Code(err), err)
	}
}

func TestConnectRejectsVersionSequenceEpochAndAckViolations(t *testing.T) {
	tests := map[string][]*gatewayv1.GatewayFrame{
		"protocol":              {func() *gatewayv1.GatewayFrame { v := hello(1); v.ProtocolVersion = 2; return v }()},
		"initial sequence":      {hello(2)},
		"replayed sequence":     {hello(1), heartbeat(1, 7, 1)},
		"wrong epoch":           {hello(1), heartbeat(2, 8, 1)},
		"wrong acknowledgement": {hello(1), heartbeat(2, 7, 0)},
		"second hello":          {hello(1), hello(2)},
	}
	for name, frames := range tests {
		t.Run(name, func(t *testing.T) {
			err := testServer(connectedStore()).Connect(&fakeStream{ctx: context.Background(), frames: frames})
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("code = %v, err = %v", status.Code(err), err)
			}
		})
	}
}

func TestConnectRejectsLifecycleWithoutOutstandingDirective(t *testing.T) {
	store := connectedStore()
	report := &gatewayv1.GatewayFrame{ProtocolVersion: ProtocolVersion, Sequence: 2, Payload: &gatewayv1.GatewayFrame_LifecycleReport{LifecycleReport: &gatewayv1.GatewayLifecycleReport{ConnectionEpoch: 7, DirectiveId: "directive-1", State: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED, Failure: gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE}}}
	err := testServer(store).Connect(&fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{hello(1), report}})
	if status.Code(err) != codes.FailedPrecondition || len(store.lifecycles) != 0 {
		t.Fatalf("code = %v lifecycle = %v err = %v", status.Code(err), store.lifecycles, err)
	}
}

func TestConnectRejectsInvalidBoundedFields(t *testing.T) {
	long := string(make([]byte, MaxGRPCEndpointBytes+1))
	tooManyCapabilities := make([]gatewayv1.GatewayCapability, MaxGatewayCapabilities+1)
	for i := range tooManyCapabilities {
		tooManyCapabilities[i] = gatewayv1.GatewayCapability_GATEWAY_CAPABILITY_SESSION_ENGINE
	}
	tests := map[string]func() []*gatewayv1.GatewayFrame{
		"empty instance": func() []*gatewayv1.GatewayFrame {
			v := hello(1)
			v.GetHello().InstanceId = ""
			return []*gatewayv1.GatewayFrame{v}
		},
		"long software version": func() []*gatewayv1.GatewayFrame {
			v := hello(1)
			v.GetHello().SoftwareVersion = string(make([]byte, MaxSoftwareVersionBytes+1))
			return []*gatewayv1.GatewayFrame{v}
		},
		"long endpoint": func() []*gatewayv1.GatewayFrame {
			v := hello(1)
			v.GetHello().GrpcEndpoint = &long
			return []*gatewayv1.GatewayFrame{v}
		},
		"invalid start time": func() []*gatewayv1.GatewayFrame {
			v := hello(1)
			v.GetHello().StartedAtUnixMs = 0
			return []*gatewayv1.GatewayFrame{v}
		},
		"unknown runtime": func() []*gatewayv1.GatewayFrame {
			v := hello(1)
			v.GetHello().RuntimeState = gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_UNKNOWN
			return []*gatewayv1.GatewayFrame{v}
		},
		"unknown capability": func() []*gatewayv1.GatewayFrame {
			v := hello(1)
			v.GetHello().Capabilities = []gatewayv1.GatewayCapability{gatewayv1.GatewayCapability_GATEWAY_CAPABILITY_UNKNOWN}
			return []*gatewayv1.GatewayFrame{v}
		},
		"duplicate capability": func() []*gatewayv1.GatewayFrame {
			v := hello(1)
			v.GetHello().Capabilities = []gatewayv1.GatewayCapability{gatewayv1.GatewayCapability_GATEWAY_CAPABILITY_SESSION_ENGINE, gatewayv1.GatewayCapability_GATEWAY_CAPABILITY_SESSION_ENGINE}
			return []*gatewayv1.GatewayFrame{v}
		},
		"too many capabilities": func() []*gatewayv1.GatewayFrame {
			v := hello(1)
			v.GetHello().Capabilities = tooManyCapabilities
			return []*gatewayv1.GatewayFrame{v}
		},
		"invalid heartbeat time": func() []*gatewayv1.GatewayFrame {
			v := heartbeat(2, 7, 1)
			v.GetHeartbeat().SentAtUnixMs = 0
			return []*gatewayv1.GatewayFrame{hello(1), v}
		},
		"invalid heartbeat runtime": func() []*gatewayv1.GatewayFrame {
			v := heartbeat(2, 7, 1)
			v.GetHeartbeat().RuntimeState = gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_UNKNOWN
			return []*gatewayv1.GatewayFrame{hello(1), v}
		},
	}
	for name, frames := range tests {
		t.Run(name, func(t *testing.T) {
			err := testServer(connectedStore()).Connect(&fakeStream{ctx: context.Background(), frames: frames()})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("code = %v, err = %v", status.Code(err), err)
			}
		})
	}
}

func TestStoreStatusIsClean(t *testing.T) {
	tests := []struct {
		err  error
		code codes.Code
	}{
		{ErrUnauthorized, codes.PermissionDenied},
		{ErrConflict, codes.Aborted},
		{ErrStaleEpoch, codes.FailedPrecondition},
		{ErrUnavailable, codes.Unavailable},
		{context.Canceled, codes.Canceled},
		{context.DeadlineExceeded, codes.DeadlineExceeded},
		{fmt.Errorf("%w: %w", ErrUnavailable, context.Canceled), codes.Canceled},
		{fmt.Errorf("%w: %w", ErrUnavailable, context.DeadlineExceeded), codes.DeadlineExceeded},
		{errors.New("database detail"), codes.Internal},
	}
	for _, tc := range tests {
		if got := status.Code(storeStatus(tc.err)); got != tc.code {
			t.Errorf("storeStatus(%v) = %v, want %v", tc.err, got, tc.code)
		}
	}
}
