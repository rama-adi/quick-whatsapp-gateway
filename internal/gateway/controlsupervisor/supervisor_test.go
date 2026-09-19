package controlsupervisor

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
)

type staticRuntime struct{ snapshot RuntimeSnapshot }

func (s staticRuntime) Snapshot() RuntimeSnapshot { return s.snapshot }

type staticDesiredState struct{}

func (staticDesiredState) ApplyDesiredState(_ context.Context, epoch uint64, snapshot *gatewayv1.DesiredStateSnapshot) (*gatewayv1.DesiredStateReport, error) {
	return &gatewayv1.DesiredStateReport{ConnectionEpoch: epoch, ProcessedRevision: snapshot.Revision, KeystoreHealth: &gatewayv1.KeystoreHealth{State: gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY}}, nil
}

type staticEventJournal struct{ acked uint64 }

func (j *staticEventJournal) NextEventBatch(context.Context, uint64) (*gatewayv1.GatewayEventBatch, error) {
	return &gatewayv1.GatewayEventBatch{Events: []*gatewayv1.GatewayEvent{{JournalSequence: 9}}}, nil
}
func (j *staticEventJournal) AckEvents(_ context.Context, sequence uint64) error {
	j.acked = sequence
	return nil
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	duration time.Duration
	ch       chan time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(duration time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{duration: duration, ch: ch})
	return ch
}

func (c *fakeClock) fire(duration time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index, timer := range c.timers {
		if timer.duration == duration {
			c.now = c.now.Add(duration)
			timer.ch <- c.now
			c.timers = append(c.timers[:index], c.timers[index+1:]...)
			return true
		}
	}
	return false
}

type fixedBackoff time.Duration

func (b fixedBackoff) Delay(uint) time.Duration { return time.Duration(b) }

type fakeStream struct {
	sent chan *gatewayv1.GatewayFrame
	recv chan receiveResult
}

func newFakeStream() *fakeStream {
	return &fakeStream{
		sent: make(chan *gatewayv1.GatewayFrame, 8),
		recv: make(chan receiveResult, 8),
	}
}

func (s *fakeStream) Send(frame *gatewayv1.GatewayFrame) error {
	s.sent <- frame
	return nil
}

func (s *fakeStream) Recv() (*gatewayv1.ControlFrame, error) {
	result := <-s.recv
	return result.frame, result.err
}

type fakeOpener struct {
	mu      sync.Mutex
	streams []*fakeStream
	opens   int
}

func (o *fakeOpener) Open(context.Context) (Stream, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.opens >= len(o.streams) {
		return nil, errors.New("no stream")
	}
	stream := o.streams[o.opens]
	o.opens++
	return stream, nil
}

func welcome(action gatewayv1.LifecycleDirectiveAction) *gatewayv1.ControlFrame {
	return &gatewayv1.ControlFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        1,
		Payload: &gatewayv1.ControlFrame_Welcome{Welcome: &gatewayv1.ControlWelcome{
			ConnectionId:        "connection-1",
			ConnectionEpoch:     7,
			HeartbeatIntervalMs: 1000,
			LeaseTimeoutMs:      5000,
			DesiredLifecycle:    action,
			ServerTimeUnixMs:    100_000,
		}},
	}
}

func heartbeatAck(controlSequence, gatewaySequence, epoch uint64) *gatewayv1.ControlFrame {
	return &gatewayv1.ControlFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        controlSequence,
		Payload: &gatewayv1.ControlFrame_HeartbeatAck{HeartbeatAck: &gatewayv1.ControlHeartbeatAck{
			AcknowledgedGatewaySequence: gatewaySequence,
			ConnectionEpoch:             epoch,
			ServerTimeUnixMs:            101_000,
		}},
	}
}

func directive(sequence uint64, action gatewayv1.LifecycleDirectiveAction) *gatewayv1.ControlFrame {
	value := int64(101_000)
	directive := &gatewayv1.LifecycleDirective{
		DirectiveId:     "directive-1",
		ConnectionEpoch: 7,
		Action:          action,
		Reason:          gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_OPERATOR,
	}
	if action == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN {
		directive.DrainDeadlineUnixMs = &value
	}
	return &gatewayv1.ControlFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        sequence,
		Payload:         &gatewayv1.ControlFrame_LifecycleDirective{LifecycleDirective: directive},
	}
}

func testSupervisor(t *testing.T, clock *fakeClock, opener StreamOpener) *Supervisor {
	t.Helper()
	supervisor, err := New(Config{
		InstanceID:      "instance-1",
		SoftwareVersion: "test",
		StartedAt:       clock.Now(),
		Runtime: staticRuntime{snapshot: RuntimeSnapshot{
			State:        gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY,
			SessionCount: 3,
		}},
		Clock:        clock,
		Backoff:      fixedBackoff(time.Second),
		MinHeartbeat: time.Second,
		MaxHeartbeat: 10 * time.Second,
	}, opener)
	if err != nil {
		t.Fatal(err)
	}
	return supervisor
}

func TestEventAckMustExactlyMatchInFlightBatch(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	stream := newFakeStream()
	stream.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)}
	supervisor := testSupervisor(t, clock, &fakeOpener{streams: []*fakeStream{stream}})
	journal := &staticEventJournal{}
	supervisor.cfg.EventJournal = journal
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- supervisor.runStream(ctx) }()
	<-stream.sent // hello
	for !clock.fire(0) {
		runtime.Gosched()
	}
	<-stream.sent // heartbeat
	batch := <-stream.sent
	if batch.GetEventBatch() == nil {
		t.Fatal("event batch was not sent")
	}
	stream.recv <- receiveResult{frame: &gatewayv1.ControlFrame{ProtocolVersion: ProtocolVersion, Sequence: 2, Payload: &gatewayv1.ControlFrame_EventAck{EventAck: &gatewayv1.GatewayEventAck{AcknowledgedJournalSequence: 8}}}}
	if err := <-done; !errors.Is(err, ErrProtocol) {
		t.Fatalf("error = %v, want protocol error", err)
	}
	if journal.acked != 0 {
		t.Fatalf("unexpected acknowledgement %d", journal.acked)
	}
}

func TestHelloAndHeartbeatSequenceEpochAndRuntime(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	stream := newFakeStream()
	stream.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)}
	supervisor := testSupervisor(t, clock, &fakeOpener{streams: []*fakeStream{stream}})
	supervisor.cfg.DesiredState = staticDesiredState{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.runStream(ctx) }()

	hello := <-stream.sent
	if hello.Sequence != 1 || hello.ProtocolVersion != ProtocolVersion || hello.GetHello() == nil {
		t.Fatalf("invalid hello: %#v", hello)
	}
	if hello.GetHello().SessionCount != 3 || hello.GetHello().RuntimeState != gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY {
		t.Fatalf("hello runtime snapshot = %#v", hello.GetHello())
	}
	if len(hello.GetHello().Capabilities) != 0 || hello.GetHello().GrpcEndpoint != nil {
		t.Fatalf("advertised unfinished capabilities: %#v", hello.GetHello())
	}
	waitStatus(t, supervisor, func(status Status) bool { return status.Connected })
	if supervisor.Status().Ready {
		t.Fatal("welcome without durable heartbeat acknowledgement reported ready")
	}
	stream.recv <- receiveResult{frame: &gatewayv1.ControlFrame{ProtocolVersion: ProtocolVersion, Sequence: 2, Payload: &gatewayv1.ControlFrame_DesiredStateSnapshot{DesiredStateSnapshot: &gatewayv1.DesiredStateSnapshot{Revision: 1}}}}
	report := <-stream.sent
	if report.Sequence != 2 || report.GetDesiredStateReport() == nil {
		t.Fatalf("desired-state report = %#v", report)
	}
	if !clock.fire(time.Second) {
		t.Fatal("heartbeat timer not registered")
	}
	heartbeat := <-stream.sent
	if heartbeat.Sequence != 3 || heartbeat.GetHeartbeat().ConnectionEpoch != 7 ||
		heartbeat.GetHeartbeat().LastControlSequence != 2 || heartbeat.GetHeartbeat().SessionCount != 3 {
		t.Fatalf("invalid heartbeat: %#v", heartbeat)
	}
	stream.recv <- receiveResult{frame: heartbeatAck(3, 3, 7)}
	waitStatus(t, supervisor, func(status Status) bool { return status.ConfirmedHeartbeat })
	if !supervisor.Status().Ready {
		t.Fatal("healthy desired-state snapshot and heartbeat did not report ready")
	}
	flushDone := make(chan error, 1)
	go func() { flushDone <- supervisor.Flush(context.Background()) }()
	flushedHeartbeat := <-stream.sent
	if flushedHeartbeat.Sequence != 4 {
		t.Fatalf("flush heartbeat sequence = %d", flushedHeartbeat.Sequence)
	}
	stream.recv <- receiveResult{frame: heartbeatAck(4, 4, 7)}
	if err := <-flushDone; err != nil {
		t.Fatal(err)
	}
	if !supervisor.Status().Stable {
		t.Fatal("two acknowledged cycles did not establish stable reconnect state")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProveCurrentConnectionCompletesOverlappingWelcomeAndHeartbeat(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	replacement := newFakeStream()
	replacement.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)}
	replacement.recv <- receiveResult{frame: heartbeatAck(2, 2, 7)}
	supervisor := testSupervisor(t, clock, &fakeOpener{streams: []*fakeStream{replacement}})

	// The proof opens the second stream without touching the incumbent stream or
	// supervisor status; callers can therefore retire the old connection only
	// after this independently authenticated exchange succeeds.
	type result struct {
		close func()
		err   error
	}
	done := make(chan result, 1)
	go func() {
		close, err := supervisor.ProveCurrentConnection(context.Background())
		done <- result{close, err}
	}()
	hello := <-replacement.sent
	if hello.Sequence != 1 || hello.GetHello() == nil {
		t.Fatalf("replacement hello = %#v", hello)
	}
	heartbeat := <-replacement.sent
	if heartbeat.Sequence != 2 || heartbeat.GetHeartbeat() == nil {
		t.Fatalf("replacement heartbeat = %#v", heartbeat)
	}
	proof := <-done
	if proof.err != nil {
		t.Fatalf("prove replacement stream: %v", proof.err)
	}
	proof.close()
	if supervisor.Status().Connected {
		t.Fatal("overlapping proof must not replace incumbent supervisor status")
	}
}

func TestDrainWelcomeIsConnectedButUnreadyAndSendsNoLifecycleReport(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	stream := newFakeStream()
	stream.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN)}
	supervisor := testSupervisor(t, clock, &fakeOpener{streams: []*fakeStream{stream}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.runStream(ctx) }()
	<-stream.sent
	waitStatus(t, supervisor, func(status Status) bool { return status.Connected })
	status := supervisor.Status()
	if status.Ready || status.DesiredLifecycle != gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN {
		t.Fatalf("unsafe drain status: %#v", status)
	}
	if !clock.fire(time.Second) {
		t.Fatal("heartbeat timer not registered")
	}
	sent := <-stream.sent
	if sent.GetHeartbeat() == nil || sent.GetLifecycleReport() != nil {
		t.Fatalf("unexpected drain response: %#v", sent)
	}
	cancel()
	<-done
}

func TestDirectiveIsExposedAndReportEchoesItsExactIdentity(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	stream := newFakeStream()
	stream.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)}
	stream.recv <- receiveResult{frame: directive(2, gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN)}
	supervisor := testSupervisor(t, clock, &fakeOpener{streams: []*fakeStream{stream}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- supervisor.runStream(ctx) }()
	<-stream.sent // Hello.

	got, err := supervisor.WaitForDirective(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "directive-1" || got.Sequence != 2 || got.ConnectionEpoch != 7 ||
		got.Action != gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN ||
		got.Reason != gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_OPERATOR ||
		got.DrainDeadline.UnixMilli() != 101_000 {
		t.Fatalf("directive = %#v", got)
	}
	wrong := got
	wrong.ID = "different-directive"
	if err := supervisor.ReportLifecycle(context.Background(), wrong,
		gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED,
		gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE); !errors.Is(err, ErrDirectiveSuperseded) {
		t.Fatalf("mismatched report error = %v, want superseded directive", err)
	}
	reported := make(chan error, 1)
	go func() {
		reported <- supervisor.ReportLifecycle(context.Background(), got,
			gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED,
			gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE)
	}()
	report := <-stream.sent
	if report.Sequence != 2 || report.GetLifecycleReport() == nil ||
		report.GetLifecycleReport().ConnectionEpoch != got.ConnectionEpoch ||
		report.GetLifecycleReport().DirectiveId != got.ID {
		t.Fatalf("lifecycle report = %#v", report)
	}
	if err := <-reported; err != nil {
		t.Fatal(err)
	}
	if status := supervisor.Status(); status.Directive != nil {
		t.Fatalf("reported directive remained active: %#v", status.Directive)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDrainDirectiveMayOmitDeadline(t *testing.T) {
	frame := directive(2, gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN)
	frame.GetLifecycleDirective().DrainDeadlineUnixMs = nil
	if err := validateControl(frame, 2, 7, 1, time.Unix(100, 0)); err != nil {
		t.Fatalf("deadline-less drain directive rejected: %v", err)
	}
}

func TestRejectsInvalidDirectiveFields(t *testing.T) {
	clock := time.Unix(100, 0)
	for name, mutate := range map[string]func(*gatewayv1.LifecycleDirective){
		"empty id":    func(v *gatewayv1.LifecycleDirective) { v.DirectiveId = "" },
		"wrong epoch": func(v *gatewayv1.LifecycleDirective) { v.ConnectionEpoch = 8 },
		"unknown action": func(v *gatewayv1.LifecycleDirective) {
			v.Action = gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_UNKNOWN
		},
		"unknown reason": func(v *gatewayv1.LifecycleDirective) {
			v.Reason = gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_UNKNOWN
		},
		"expired deadline": func(v *gatewayv1.LifecycleDirective) {
			expired := int64(100_000)
			v.DrainDeadlineUnixMs = &expired
		},
		"deadline on run": func(v *gatewayv1.LifecycleDirective) {
			v.Action = gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN
		},
	} {
		t.Run(name, func(t *testing.T) {
			frame := directive(2, gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN)
			mutate(frame.GetLifecycleDirective())
			if err := validateControl(frame, 2, 7, 1, clock); !errors.Is(err, ErrProtocol) {
				t.Fatalf("validateControl error = %v, want protocol violation", err)
			}
		})
	}
}

func TestRejectsControlSequenceAndEpoch(t *testing.T) {
	for name, mutate := range map[string]func(*gatewayv1.ControlFrame){
		"sequence": func(frame *gatewayv1.ControlFrame) { frame.Sequence = 3 },
		"epoch": func(frame *gatewayv1.ControlFrame) {
			frame.GetLifecycleDirective().ConnectionEpoch = 8
		},
	} {
		t.Run(name, func(t *testing.T) {
			clock := &fakeClock{now: time.Unix(100, 0)}
			stream := newFakeStream()
			stream.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)}
			directive := directive(2, gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN)
			mutate(directive)
			stream.recv <- receiveResult{frame: directive}
			supervisor := testSupervisor(t, clock, &fakeOpener{streams: []*fakeStream{stream}})
			err := supervisor.runStream(context.Background())
			if err == nil {
				t.Fatal("invalid control frame accepted")
			}
		})
	}
}

func TestRejectsInvalidWelcome(t *testing.T) {
	for name, mutate := range map[string]func(*gatewayv1.ControlFrame){
		"protocol": func(frame *gatewayv1.ControlFrame) { frame.ProtocolVersion++ },
		"sequence": func(frame *gatewayv1.ControlFrame) { frame.Sequence++ },
		"epoch": func(frame *gatewayv1.ControlFrame) {
			frame.GetWelcome().ConnectionEpoch = 0
		},
		"connection": func(frame *gatewayv1.ControlFrame) {
			frame.GetWelcome().ConnectionId = ""
		},
		"server time": func(frame *gatewayv1.ControlFrame) {
			frame.GetWelcome().ServerTimeUnixMs = 0
		},
		"heartbeat": func(frame *gatewayv1.ControlFrame) {
			frame.GetWelcome().HeartbeatIntervalMs = 0
		},
		"lease": func(frame *gatewayv1.ControlFrame) {
			frame.GetWelcome().LeaseTimeoutMs = 1000
		},
		"lifecycle": func(frame *gatewayv1.ControlFrame) {
			frame.GetWelcome().DesiredLifecycle = gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_UNKNOWN
		},
	} {
		t.Run(name, func(t *testing.T) {
			clock := &fakeClock{now: time.Unix(100, 0)}
			stream := newFakeStream()
			frame := welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)
			mutate(frame)
			stream.recv <- receiveResult{frame: frame}
			supervisor := testSupervisor(t, clock, &fakeOpener{streams: []*fakeStream{stream}})
			if err := supervisor.runStream(context.Background()); err == nil {
				t.Fatal("invalid welcome accepted")
			}
		})
	}
}

func TestRunBacksOffReconnectsAndCancelsCleanly(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	first := newFakeStream()
	first.recv <- receiveResult{err: errors.New("offline")}
	second := newFakeStream()
	second.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)}
	opener := &fakeOpener{streams: []*fakeStream{first, second}}
	supervisor := testSupervisor(t, clock, opener)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	<-first.sent
	waitFor(t, func() bool { return clock.fire(time.Second) })
	<-second.sent
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if supervisor.Status().Ready || supervisor.Status().Connected {
		t.Fatalf("shutdown retained readiness: %#v", supervisor.Status())
	}
}

// TestLeaseExpiryReconnectsWithBackoffAndReplaysUnackedEvents pins the soak
// behavior behind acknowledgement loss: a silent API (lease expires with no
// inbound frame) tears the stream down unready and, on the replacement stream,
// the journal replays its still-unacknowledged batch. Bounded: one expiry, a
// handful of backoff fires, no sleeps.
func TestLeaseExpiryReconnectsWithBackoffAndReplaysUnackedEvents(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	first := newFakeStream()
	second := newFakeStream()
	first.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)}
	second.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)}
	opener := &fakeOpener{streams: []*fakeStream{first, second}}
	supervisor := testSupervisor(t, clock, opener)
	journal := &staticEventJournal{}
	supervisor.cfg.EventJournal = journal
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()

	<-first.sent // hello; the heartbeat timer fires at 0 so the journal batch goes out too
	waitFor(t, func() bool { return clock.fire(0) })
	<-first.sent // heartbeat
	batch := <-first.sent
	if batch.GetEventBatch() == nil || batch.GetEventBatch().Events[0].JournalSequence != 9 {
		t.Fatalf("first-stream journal batch = %#v", batch)
	}

	// The API never answers: the advertised 5-second lease expires.
	if !clock.fire(5 * time.Second) {
		t.Fatal("lease timer not registered")
	}
	waitStatus(t, supervisor, func(status Status) bool { return !status.Connected && !status.Ready })

	// Backoff re-arms every attempt; fire it (spinning so the supervisor can
	// consume each timer) until the replacement stream opens, then read its
	// hello and the immediate replay of the still-unacked batch.
	for range 10 {
		waitFor(t, func() bool { return clock.fire(time.Second) })
		select {
		case f := <-second.sent:
			if f.GetHello() == nil {
				t.Fatalf("replacement first frame not hello: %#v", f)
			}
			waitFor(t, func() bool { return clock.fire(0) })
			<-second.sent // heartbeat
			replayed := <-second.sent
			if replayed.GetEventBatch() == nil || replayed.GetEventBatch().Events[0].JournalSequence != 9 {
				t.Fatalf("reconnect did not replay the unacked batch: %#v", replayed)
			}
			if journal.acked != 0 {
				t.Fatalf("acknowledged watermark advanced without an ack: %d", journal.acked)
			}
			cancel()
			return
		default:
		}
	}
	t.Fatal("supervisor never reopened the control stream after lease expiry")
}

func TestStartingRuntimeNeverBecomesReady(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	stream := newFakeStream()
	stream.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)}
	supervisor, err := New(Config{
		InstanceID:       "instance-1",
		SoftwareVersion:  "test",
		StartedAt:        clock.Now(),
		Runtime:          staticRuntime{snapshot: RuntimeSnapshot{State: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_STARTING}},
		Clock:            clock,
		Backoff:          fixedBackoff(time.Second),
		MinHeartbeat:     time.Second,
		MaxHeartbeat:     10 * time.Second,
		HandshakeTimeout: 10 * time.Second,
	}, &fakeOpener{streams: []*fakeStream{stream}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.runStream(ctx) }()
	<-stream.sent
	waitStatus(t, supervisor, func(status Status) bool { return status.Connected })
	if supervisor.Status().Ready {
		t.Fatal("starting runtime reported ready")
	}
	cancel()
	<-done
}

func TestRunIsSingleUseAndProtocolFailureIsTerminal(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	stream := newFakeStream()
	badWelcome := welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)
	badWelcome.ProtocolVersion++
	stream.recv <- receiveResult{frame: badWelcome}
	opener := &fakeOpener{streams: []*fakeStream{stream}}
	supervisor := testSupervisor(t, clock, opener)
	err := supervisor.Run(context.Background())
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("Run error = %v, want protocol violation", err)
	}
	if opener.opens != 1 {
		t.Fatalf("terminal protocol error opened %d streams", opener.opens)
	}
	if err = supervisor.Run(context.Background()); err == nil {
		t.Fatal("second Run succeeded")
	}
}

func TestWelcomeTimeoutIsBoundedAndRetryable(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	stream := newFakeStream()
	supervisor := testSupervisor(t, clock, &fakeOpener{streams: []*fakeStream{stream}})
	done := make(chan error, 1)
	go func() { done <- supervisor.runStream(context.Background()) }()
	<-stream.sent
	// The first matching timer belongs to the completed Hello send; the second
	// bounds the Welcome receive without placing a deadline on the stream.
	waitFor(t, func() bool { return clock.fire(10 * time.Second) })
	waitFor(t, func() bool { return clock.fire(10 * time.Second) })
	if err := <-done; err == nil || errors.Is(err, ErrProtocol) {
		t.Fatalf("welcome timeout = %v", err)
	}
}

func waitStatus(t *testing.T, supervisor *Supervisor, predicate func(Status) bool) {
	t.Helper()
	waitFor(t, func() bool { return predicate(supervisor.Status()) })
}

func waitFor(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met")
		}
		time.Sleep(time.Millisecond)
	}
}

type assignmentAwareJournal struct {
	staticEventJournal
	reset   chan struct{}
	applied chan uint64
}

func (j *assignmentAwareJournal) ResetDesiredAssignments() { close(j.reset) }
func (j *assignmentAwareJournal) SetDesiredAssignments(snapshot *gatewayv1.DesiredStateSnapshot) {
	j.applied <- snapshot.Revision
}

func TestJournalOwnershipUpdatesOnlyAfterApplyingSnapshot(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	stream := newFakeStream()
	stream.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)}
	supervisor := testSupervisor(t, clock, &fakeOpener{streams: []*fakeStream{stream}})
	journal := &assignmentAwareJournal{reset: make(chan struct{}), applied: make(chan uint64, 1)}
	supervisor.cfg.EventJournal = journal
	supervisor.cfg.DesiredState = staticDesiredState{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- supervisor.runStream(ctx) }()
	<-stream.sent // hello
	<-journal.reset
	select {
	case <-journal.applied:
		t.Fatal("journal inherited ownership before a fresh snapshot")
	default:
	}
	stream.recv <- receiveResult{frame: &gatewayv1.ControlFrame{
		ProtocolVersion: ProtocolVersion, Sequence: 2,
		Payload: &gatewayv1.ControlFrame_DesiredStateSnapshot{
			DesiredStateSnapshot: &gatewayv1.DesiredStateSnapshot{Revision: 1},
		},
	}}
	report := <-stream.sent
	if report.GetDesiredStateReport() == nil {
		t.Fatal("snapshot not acknowledged")
	}
	if revision := <-journal.applied; revision != 1 {
		t.Fatalf("journal revision = %d", revision)
	}
	cancel()
	<-done
}
