package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeStore struct {
	connection            Connection
	acceptErr             error
	writeErr              error
	desired               DesiredLifecycle
	desiredState          DesiredState
	desiredStateAcks      []uint64
	acceptedID            string
	heartbeats            []uint64
	lifecycles            []uint64
	lifecycleReports      []LifecycleReport
	disconnect            []uint64
	disconnectHasDeadline bool
	// ingest records committed event batches and can fail the ack send.
	ingested      [][]GatewayEvent
	ingestErr     error
	ackSends      []uint64
	failEventAckN int // number of EventAck sends to fail before succeeding
}

func (s *fakeStore) Accept(_ context.Context, id string, _ Hello) (Connection, error) {
	s.acceptedID = id
	return s.connection, s.acceptErr
}
func (s *fakeStore) Heartbeat(_ context.Context, _ string, epoch uint64, _ Heartbeat) (DesiredLifecycle, error) {
	s.heartbeats = append(s.heartbeats, epoch)
	return s.desired, s.writeErr
}
func (s *fakeStore) DesiredState(_ context.Context, _ string, _ uint64, revision uint64, leaseExpiresAt time.Time) (DesiredState, error) {
	state := s.desiredState
	state.Revision = revision
	for i := range state.Assignments {
		state.Assignments[i].LeaseExpiresAt = leaseExpiresAt
	}
	return state, s.writeErr
}
func (s *fakeStore) PersistDesiredStateReport(_ context.Context, _ string, _ uint64, report ReconciliationReport) error {
	s.desiredStateAcks = append(s.desiredStateAcks, report.Revision)
	return s.writeErr
}
func (s *fakeStore) IngestEvents(_ context.Context, _ string, _ uint64, events []GatewayEvent) error {
	if s.ingestErr != nil {
		return s.ingestErr
	}
	s.ingested = append(s.ingested, events)
	return nil
}
func (s *fakeStore) Lifecycle(_ context.Context, _ string, epoch uint64, report LifecycleReport) error {
	s.lifecycles = append(s.lifecycles, epoch)
	s.lifecycleReports = append(s.lifecycleReports, report)
	return s.writeErr
}
func (s *fakeStore) Disconnect(ctx context.Context, _ string, epoch uint64) error {
	s.disconnect = append(s.disconnect, epoch)
	_, s.disconnectHasDeadline = ctx.Deadline()
	return nil
}

type fakeStream struct {
	ctx    context.Context
	frames []*gatewayv1.GatewayFrame
	sent   []*gatewayv1.ControlFrame
	send   func(*gatewayv1.ControlFrame) error
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
	if s.send != nil {
		return s.send(frame)
	}
	return nil
}

// eventAckSend models a transport that fails the first N EventAck sends (the
// acknowledgement is lost after the ingest commit) and succeeds afterwards.
func (s *fakeStream) failEventAcksBefore(n int) {
	failures := 0
	s.send = func(frame *gatewayv1.ControlFrame) error {
		if frame.GetEventAck() == nil || failures >= n {
			return nil
		}
		failures++
		return status.Error(codes.Unavailable, "ack lost in transit")
	}
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
	return &fakeStore{connection: Connection{ID: "conn-1", Epoch: 7, HeartbeatInterval: 5 * time.Second, LeaseTimeout: 15 * time.Second, DesiredLifecycle: gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN, DesiredRevision: 1}, desired: DesiredLifecycle{Action: gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN, Revision: 1}}
}

func TestHeartbeatAckIsSentOnlyAfterPersistence(t *testing.T) {
	store := connectedStore()
	stream := &fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{hello(1), heartbeat(2, 7, 1)}}
	stream.send = func(frame *gatewayv1.ControlFrame) error {
		if frame.GetHeartbeatAck() != nil && len(store.heartbeats) != 1 {
			t.Fatal("heartbeat ack sent before persistence completed")
		}
		return nil
	}
	if err := testServer(store).Connect(stream); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatPersistenceErrorSendsNoAck(t *testing.T) {
	store := connectedStore()
	store.writeErr = ErrUnavailable
	stream := &fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{hello(1), heartbeat(2, 7, 1)}}
	err := testServer(store).Connect(stream)
	if status.Code(err) != codes.Unavailable || len(stream.sent) != 1 || stream.sent[0].GetWelcome() == nil {
		t.Fatalf("err = %v, control frames = %+v", err, stream.sent)
	}
}

func TestConnectAllowsControlAckLagButRejectsFutureAndRegression(t *testing.T) {
	t.Run("lag", func(t *testing.T) {
		store := connectedStore()
		stream := &fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{
			hello(1), heartbeat(2, 7, 1), heartbeat(3, 7, 1), heartbeat(4, 7, 2),
		}}
		if err := testServer(store).Connect(stream); err != nil {
			t.Fatal(err)
		}
		if len(stream.sent) != 8 || stream.sent[7].Sequence != 8 {
			t.Fatalf("control frames = %+v", stream.sent)
		}
	})
	t.Run("future", func(t *testing.T) {
		err := testServer(connectedStore()).Connect(&fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{
			hello(1), heartbeat(2, 7, 1), heartbeat(3, 7, 5),
		}})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("code = %v, err = %v", status.Code(err), err)
		}
	})
	t.Run("regression", func(t *testing.T) {
		err := testServer(connectedStore()).Connect(&fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{
			hello(1), heartbeat(2, 7, 1), heartbeat(3, 7, 2), heartbeat(4, 7, 1),
		}})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("code = %v, err = %v", status.Code(err), err)
		}
	})
}

type blockingStream struct {
	*fakeStream
	recv chan *gatewayv1.GatewayFrame
}

func (s *blockingStream) Recv() (*gatewayv1.GatewayFrame, error) {
	select {
	case frame := <-s.recv:
		return frame, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func TestConnectHelloDeadlineWhileRecvIsBlocked(t *testing.T) {
	store := connectedStore()
	server := testServer(store)
	server.HelloTimeout = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &blockingStream{fakeStream: &fakeStream{ctx: ctx}, recv: make(chan *gatewayv1.GatewayFrame)}
	err := server.Connect(stream)
	if status.Code(err) != codes.DeadlineExceeded || store.acceptedID != "" {
		t.Fatalf("code = %v accepted = %q err = %v", status.Code(err), store.acceptedID, err)
	}
}

func TestDisconnectCleanupContextIsDetachedAndBounded(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	server := &Server{DisconnectTimeout: time.Second}
	ctx, cancel := server.disconnectContext(parent)
	defer cancel()
	if ctx.Err() != nil {
		t.Fatalf("cleanup inherited parent cancellation: %v", ctx.Err())
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Second {
		t.Fatalf("cleanup deadline = %v, ok=%v", deadline, ok)
	}
}

func TestConnectLeaseExpiresWhileRecvIsBlocked(t *testing.T) {
	store := connectedStore()
	store.connection.LeaseTimeout = 10 * time.Millisecond
	server := testServer(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recv := make(chan *gatewayv1.GatewayFrame, 1)
	recv <- hello(1)
	stream := &blockingStream{fakeStream: &fakeStream{ctx: ctx}, recv: recv}
	err := server.Connect(stream)
	if status.Code(err) != codes.DeadlineExceeded || len(store.disconnect) != 1 {
		t.Fatalf("code = %v disconnects = %v err = %v", status.Code(err), store.disconnect, err)
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

func lifecycleReport(sequence, epoch uint64, directiveID string, state gatewayv1.GatewayRuntimeState, failure gatewayv1.LifecycleFailure) *gatewayv1.GatewayFrame {
	return &gatewayv1.GatewayFrame{ProtocolVersion: ProtocolVersion, Sequence: sequence, Payload: &gatewayv1.GatewayFrame_LifecycleReport{LifecycleReport: &gatewayv1.GatewayLifecycleReport{
		ConnectionEpoch: epoch, DirectiveId: directiveID, State: state, Failure: failure,
	}}}
}

func TestConnectEmitsAndDurablyAcknowledgesLifecycleDirective(t *testing.T) {
	store := connectedStore()
	store.desired = DesiredLifecycle{Action: gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN, Revision: 2}
	stream := &fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{
		hello(1), heartbeat(2, 7, 1),
		lifecycleReport(3, 7, "conn-1:2", gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED, gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE),
		heartbeat(4, 7, 3),
	}}
	if err := testServer(store).Connect(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 7 || stream.sent[2].GetHeartbeatAck() == nil {
		t.Fatalf("control frames = %+v", stream.sent)
	}
	directive := stream.sent[3].GetLifecycleDirective()
	if stream.sent[3].Sequence != 4 || directive == nil || directive.DirectiveId != "conn-1:2" || directive.ConnectionEpoch != 7 || directive.Action != gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN || directive.Reason != gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_OPERATOR {
		t.Fatalf("directive = %+v", directive)
	}
	if len(store.lifecycleReports) != 1 || store.lifecycleReports[0].DirectiveID != directive.DirectiveId || stream.sent[5].GetHeartbeatAck().AcknowledgedGatewaySequence != 4 {
		t.Fatalf("lifecycle reports = %+v, ack = %+v", store.lifecycleReports, stream.sent[5])
	}
}

func TestConnectSendsAuthoritativeDesiredStateAndFencesAcknowledgement(t *testing.T) {
	store := connectedStore()
	store.connection.DesiredRevision = 9
	store.desiredState = DesiredState{Assignments: []DesiredSession{{
		SessionID: "session_1", OrganizationID: "org_1", DeviceJID: "15551234567@s.whatsapp.net", AssignmentEpoch: 4,
		DesiredAction:  gatewayv1.SessionDesiredAction_SESSION_DESIRED_ACTION_RUN,
		ConfigRevision: 9, AutoRead: true, RatePerMin: 20, RatePerHour: 200,
	}}}
	ack := &gatewayv1.GatewayFrame{ProtocolVersion: ProtocolVersion, Sequence: 2, Payload: &gatewayv1.GatewayFrame_DesiredStateReport{DesiredStateReport: &gatewayv1.DesiredStateReport{ConnectionEpoch: 7, ProcessedRevision: 9, KeystoreHealth: &gatewayv1.KeystoreHealth{State: gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY}}}}
	stream := &fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{hello(1), ack}}
	if err := testServer(store).Connect(stream); err != nil {
		t.Fatal(err)
	}
	snapshot := stream.sent[1].GetDesiredStateSnapshot()
	if snapshot == nil || snapshot.Revision != 9 || len(snapshot.Assignments) != 1 || snapshot.Assignments[0].OrganizationId != "org_1" || snapshot.Assignments[0].AssignmentEpoch != 4 || snapshot.Assignments[0].LeaseExpiresAtUnixMs <= 1234 || snapshot.Assignments[0].GetConfig().Revision != 9 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(store.desiredStateAcks) != 1 || store.desiredStateAcks[0] != 9 {
		t.Fatalf("desired state acknowledgements = %v", store.desiredStateAcks)
	}
}

func TestConnectRejectsDesiredStateAckForAnotherEpoch(t *testing.T) {
	ack := &gatewayv1.GatewayFrame{ProtocolVersion: ProtocolVersion, Sequence: 2, Payload: &gatewayv1.GatewayFrame_DesiredStateReport{DesiredStateReport: &gatewayv1.DesiredStateReport{ConnectionEpoch: 8, KeystoreHealth: &gatewayv1.KeystoreHealth{State: gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY}}}}
	err := testServer(connectedStore()).Connect(&fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{hello(1), ack}})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v, err = %v", status.Code(err), err)
	}
}

// journalEventBatch is one journal batch whose single event carries the given
// sequence and id, valid for gateway gw_cert on epoch 7.
func journalEventBatch(sequence uint64, id string) *gatewayv1.GatewayEventBatch {
	return &gatewayv1.GatewayEventBatch{Events: []*gatewayv1.GatewayEvent{{
		JournalSequence: sequence, EventId: id, GatewayId: "gw_cert", ConnectionEpoch: 7,
		AssignmentEpoch: 2, SessionId: "s1", OrganizationId: "o1", EventType: "message", OccurredAtUnixMs: 1,
	}}}
}

func lastEventAck(t *testing.T, stream *fakeStream) *gatewayv1.ControlFrame {
	t.Helper()
	for i := len(stream.sent) - 1; i >= 0; i-- {
		if frame := stream.sent[i]; frame.GetEventAck() != nil {
			return frame
		}
	}
	return nil
}

// secondAckWatermark returns the watermark of the reconnect stream's EventAck.
func secondAckWatermark(t *testing.T, stream *fakeStream) uint64 {
	t.Helper()
	frame := lastEventAck(t, stream)
	if frame == nil {
		t.Fatal("reconnect emitted no EventAck")
	}
	return frame.GetEventAck().AcknowledgedJournalSequence
}

func TestEventBatchValidation(t *testing.T) {
	valid := func() *gatewayv1.GatewayEventBatch {
		return &gatewayv1.GatewayEventBatch{Events: []*gatewayv1.GatewayEvent{{JournalSequence: 1, EventId: "e1", GatewayId: "gw_cert", ConnectionEpoch: 7, AssignmentEpoch: 2, SessionId: "s1", OrganizationId: "o1", EventType: "message", OccurredAtUnixMs: 1}}}
	}
	if events, err := eventBatch(valid(), "gw_cert", 7); err != nil || len(events) != 1 {
		t.Fatalf("valid=%v %v", events, err)
	}
	for name, mutate := range map[string]func(*gatewayv1.GatewayEventBatch){
		"zero sequence": func(b *gatewayv1.GatewayEventBatch) { b.Events[0].JournalSequence = 0 },
		"wrong gateway": func(b *gatewayv1.GatewayEventBatch) { b.Events[0].GatewayId = "other" },
		"stale epoch":   func(b *gatewayv1.GatewayEventBatch) { b.Events[0].ConnectionEpoch = 8 },
		"oversize":      func(b *gatewayv1.GatewayEventBatch) { b.Events[0].Payload = make([]byte, MaxGatewayEventBatchBytes+1) },
	} {
		t.Run(name, func(t *testing.T) {
			b := valid()
			mutate(b)
			if _, err := eventBatch(b, "gw_cert", 7); status.Code(err) == codes.OK {
				t.Fatal("accepted")
			}
		})
	}
	b := valid()
	b.Events = append(b.Events, &gatewayv1.GatewayEvent{JournalSequence: 1, EventId: "e2", GatewayId: "gw_cert", ConnectionEpoch: 7, AssignmentEpoch: 2, SessionId: "s1", OrganizationId: "o1", EventType: "message", OccurredAtUnixMs: 1})
	if _, err := eventBatch(b, "gw_cert", 7); status.Code(err) == codes.OK {
		t.Fatal("nonmonotonic accepted")
	}
}

func TestConnectRejectsInvalidLifecycleDirectiveAcknowledgements(t *testing.T) {
	invalid := map[string]func(*gatewayv1.GatewayLifecycleReport){
		"wrong epoch":     func(report *gatewayv1.GatewayLifecycleReport) { report.ConnectionEpoch = 8 },
		"wrong directive": func(report *gatewayv1.GatewayLifecycleReport) { report.DirectiveId = "other" },
		"unknown failure": func(report *gatewayv1.GatewayLifecycleReport) {
			report.Failure = gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_UNKNOWN
		},
		"successful drain not drained": func(report *gatewayv1.GatewayLifecycleReport) {
			report.State = gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING
		},
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			store := connectedStore()
			store.desired = DesiredLifecycle{Action: gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN, Revision: 2}
			report := lifecycleReport(3, 7, "conn-1:1", gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED, gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE)
			mutate(report.GetLifecycleReport())
			err := testServer(store).Connect(&fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{hello(1), heartbeat(2, 7, 1), report}})
			if len(store.lifecycleReports) != 0 {
				t.Fatalf("persisted invalid report: %+v", store.lifecycleReports)
			}
			if status.Code(err) != codes.FailedPrecondition && status.Code(err) != codes.InvalidArgument {
				t.Fatalf("code = %v, err = %v", status.Code(err), err)
			}
		})
	}
}

func TestConnectDoesNotSendLifecycleDirectiveBeforeHeartbeatPersistence(t *testing.T) {
	store := connectedStore()
	store.desired = DesiredLifecycle{Action: gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN, Revision: 2}
	stream := &fakeStream{ctx: context.Background(), frames: []*gatewayv1.GatewayFrame{hello(1), heartbeat(2, 7, 1)}}
	stream.send = func(frame *gatewayv1.ControlFrame) error {
		if frame.GetLifecycleDirective() != nil && len(store.heartbeats) != 1 {
			t.Fatal("lifecycle directive sent before heartbeat persistence")
		}
		return nil
	}
	if err := testServer(store).Connect(stream); err != nil {
		t.Fatal(err)
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
		"invalid HTTP base URL": func() []*gatewayv1.GatewayFrame {
			v := hello(1)
			raw := "https://user@gateway.test/path?query=yes#fragment"
			v.GetHello().HttpBaseUrl = &raw
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
