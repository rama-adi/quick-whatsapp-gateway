package controlsupervisor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
)

type staticRuntime struct{ snapshot RuntimeSnapshot }

func (s staticRuntime) Snapshot() RuntimeSnapshot { return s.snapshot }

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

func TestHelloAndHeartbeatSequenceEpochAndRuntime(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	stream := newFakeStream()
	stream.recv <- receiveResult{frame: welcome(gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN)}
	supervisor := testSupervisor(t, clock, &fakeOpener{streams: []*fakeStream{stream}})
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
	if !clock.fire(time.Second) {
		t.Fatal("heartbeat timer not registered")
	}
	heartbeat := <-stream.sent
	if heartbeat.Sequence != 2 || heartbeat.GetHeartbeat().ConnectionEpoch != 7 ||
		heartbeat.GetHeartbeat().LastControlSequence != 1 || heartbeat.GetHeartbeat().SessionCount != 3 {
		t.Fatalf("invalid heartbeat: %#v", heartbeat)
	}
	stream.recv <- receiveResult{frame: heartbeatAck(2, 2, 7)}
	waitStatus(t, supervisor, func(status Status) bool { return status.Ready })
	flushDone := make(chan error, 1)
	go func() { flushDone <- supervisor.Flush(context.Background()) }()
	flushedHeartbeat := <-stream.sent
	if flushedHeartbeat.Sequence != 3 {
		t.Fatalf("flush heartbeat sequence = %d", flushedHeartbeat.Sequence)
	}
	stream.recv <- receiveResult{frame: heartbeatAck(3, 3, 7)}
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
			directive := &gatewayv1.ControlFrame{
				ProtocolVersion: ProtocolVersion,
				Sequence:        2,
				Payload: &gatewayv1.ControlFrame_LifecycleDirective{LifecycleDirective: &gatewayv1.LifecycleDirective{
					DirectiveId:     "directive-1",
					ConnectionEpoch: 7,
					Action:          gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN,
				}},
			}
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
