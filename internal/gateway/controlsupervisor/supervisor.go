// Package controlsupervisor maintains the gateway's long-lived control stream.
package controlsupervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const ProtocolVersion uint32 = 1

var (
	ErrProtocol            = errors.New("control supervisor: protocol violation")
	ErrDirectiveSuperseded = errors.New("control supervisor: lifecycle directive superseded")
)

type RuntimeSnapshot struct {
	State        gatewayv1.GatewayRuntimeState
	SessionCount uint32
}

type RuntimeSource interface {
	Snapshot() RuntimeSnapshot
}

// DesiredStateApplier applies one complete authoritative desired-state snapshot.
// Apply must not return until the snapshot has been made effective locally: the
// supervisor sends its acknowledgement only after that point.
type DesiredStateApplier interface {
	ApplyDesiredState(context.Context, uint64, *gatewayv1.DesiredStateSnapshot) (*gatewayv1.DesiredStateReport, error)
}

// EventJournal supplies durable batches and advances only the API's committed
// acknowledgement cursor. It is intentionally transport-agnostic.
type EventJournal interface {
	NextEventBatch(context.Context, uint64) (*gatewayv1.GatewayEventBatch, error)
	AckEvents(context.Context, uint64) error
}

// JournalPressure is one observation of local event-journal backpressure for
// heartbeat telemetry. It mirrors the journal's capacity states without making
// the supervisor import the journal package.
type JournalPressure struct {
	State   gatewayv1.GatewayJournalState
	Entries uint64
	Bytes   uint64
}

type JournalMetricsSource func(context.Context) (JournalPressure, error)

type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type Backoff interface {
	Delay(attempt uint) time.Duration
}

type Stream interface {
	Send(*gatewayv1.GatewayFrame) error
	Recv() (*gatewayv1.ControlFrame, error)
}

type StreamOpener interface {
	Open(context.Context) (Stream, error)
}

type Config struct {
	InstanceID       string
	SoftwareVersion  string
	HTTPBaseURL      string
	GRPCEndpoint     string
	StartedAt        time.Time
	Runtime          RuntimeSource
	DesiredState     DesiredStateApplier
	EventJournal     EventJournal
	JournalMetrics   JournalMetricsSource
	Clock            Clock
	Backoff          Backoff
	MinHeartbeat     time.Duration
	MaxHeartbeat     time.Duration
	HandshakeTimeout time.Duration
}

type Status struct {
	Ready                bool
	Connected            bool
	ConfirmedHeartbeat   bool
	Stable               bool
	ConnectionEpoch      uint64
	LastControlSequence  uint64
	AcknowledgedRuntime  gatewayv1.GatewayRuntimeState
	DesiredLifecycle     gatewayv1.LifecycleDirectiveAction
	DesiredStateRevision uint64
	DesiredStateApplied  bool
	DesiredStateHealthy  bool
	Directive            *LifecycleDirective
	LastError            error
}

// LifecycleDirective is a validated API lifecycle instruction. Unlike the
// desired lifecycle in Welcome, it has an identity that must be echoed in the
// one lifecycle report for this connection epoch.
type LifecycleDirective struct {
	ID              string
	ConnectionEpoch uint64
	Sequence        uint64
	Action          gatewayv1.LifecycleDirectiveAction
	DrainDeadline   time.Time
	Reason          gatewayv1.LifecycleDirectiveReason
}

type lifecycleReportRequest struct {
	directive LifecycleDirective
	state     gatewayv1.GatewayRuntimeState
	failure   gatewayv1.LifecycleFailure
	result    chan error
}

type Supervisor struct {
	cfg              Config
	opener           StreamOpener
	mu               sync.RWMutex
	status           Status
	running          atomic.Bool
	report           chan struct{}
	lifecycleReports chan lifecycleReportRequest
	changed          chan struct{}
}

func New(cfg Config, opener StreamOpener) (*Supervisor, error) {
	if opener == nil || cfg.InstanceID == "" || cfg.SoftwareVersion == "" || cfg.Runtime == nil {
		return nil, errors.New("control supervisor: invalid configuration")
	}
	if cfg.HTTPBaseURL != "" && !validHTTPBaseURL(cfg.HTTPBaseURL) {
		return nil, errors.New("control supervisor: invalid HTTP base URL")
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.Backoff == nil {
		cfg.Backoff = ExponentialBackoff{Min: 250 * time.Millisecond, Max: 30 * time.Second, Jitter: 0.2}
	}
	if cfg.StartedAt.IsZero() {
		cfg.StartedAt = cfg.Clock.Now()
	}
	if cfg.MinHeartbeat <= 0 {
		cfg.MinHeartbeat = 100 * time.Millisecond
	}
	if cfg.MaxHeartbeat <= 0 {
		cfg.MaxHeartbeat = time.Minute
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.MinHeartbeat > cfg.MaxHeartbeat {
		return nil, errors.New("control supervisor: invalid heartbeat bounds")
	}
	return &Supervisor{
		cfg:    cfg,
		opener: opener,

		report:           make(chan struct{}, 1),
		lifecycleReports: make(chan lifecycleReportRequest),
		changed:          make(chan struct{}),
	}, nil
}

func (s *Supervisor) WaitForWelcome(ctx context.Context) (Status, error) {
	for {
		s.mu.RLock()
		if s.status.Connected {
			status := copyStatus(s.status)
			s.mu.RUnlock()
			return status, nil
		}
		changed := s.changed
		s.mu.RUnlock()
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-changed:
		}
	}
}

// SetDesiredState installs the snapshot applier before Run starts. It exists to
// let the composition root construct the local session engine before opening
// the control stream.
func (s *Supervisor) SetDesiredState(applier DesiredStateApplier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.DesiredState = applier
}

// WaitForDesiredState waits for an authoritative snapshot to be successfully
// applied on the current connection. A Welcome alone never authorizes engine
// work, because it contains no session ownership information.
func (s *Supervisor) WaitForDesiredState(ctx context.Context, epoch uint64) (Status, error) {
	for {
		s.mu.RLock()
		applied := s.status.Connected &&
			s.status.ConnectionEpoch == epoch &&
			s.status.DesiredStateApplied
		if applied {
			status := copyStatus(s.status)
			s.mu.RUnlock()
			return status, nil
		}
		changed := s.changed
		s.mu.RUnlock()
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-changed:
		}
	}
}

// MarkDesiredStateUnhealthy closes admission immediately after a local lease
// expires. Only a later successfully applied desired-state report can reopen it.
func (s *Supervisor) MarkDesiredStateUnhealthy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.DesiredStateHealthy = false
	s.status.Ready = false
	s.signalChangedLocked()
}

// ProveCurrentConnection opens an overlapping stream through the current
// opener and proves it has completed the full authenticated control handshake.
// It intentionally does not mutate the incumbent stream's status: callers use
// it to decide whether retiring that incumbent connection is safe.
func (s *Supervisor) ProveCurrentConnection(ctx context.Context) (func(), error) {
	stream, err := s.opener.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("open replacement control stream: %w", err)
	}
	runtime := s.cfg.Runtime.Snapshot()
	if !validRuntimeState(runtime.State) {
		return nil, fmt.Errorf("%w: invalid runtime state", ErrProtocol)
	}
	probeCtx, cancel := context.WithCancel(ctx)
	if err := s.sendHandshake(probeCtx, cancel, stream, s.newHelloFrame(runtime)); err != nil {
		cancel()
		return nil, fmt.Errorf("send replacement hello: %w", err)
	}
	welcome, first, err := s.receiveReplacementWelcome(ctx, cancel, stream)
	if err != nil {
		cancel()
		return nil, err
	}
	if err := s.probeReplacementHeartbeat(probeCtx, cancel, stream, welcome, first, runtime); err != nil {
		cancel()
		return nil, err
	}
	return cancel, nil
}

// newHelloFrame builds the sequence-1 Hello advertising the given runtime.
func (s *Supervisor) newHelloFrame(runtime RuntimeSnapshot) *gatewayv1.GatewayFrame {
	hello := &gatewayv1.GatewayHello{
		InstanceId:      s.cfg.InstanceID,
		SoftwareVersion: s.cfg.SoftwareVersion,
		StartedAtUnixMs: s.cfg.StartedAt.UnixMilli(),
		SessionCount:    runtime.SessionCount,
		RuntimeState:    runtime.State,
	}
	if s.cfg.HTTPBaseURL != "" {
		hello.HttpBaseUrl = &s.cfg.HTTPBaseURL
	}
	if s.cfg.GRPCEndpoint != "" {
		hello.GrpcEndpoint = &s.cfg.GRPCEndpoint
	}
	return &gatewayv1.GatewayFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        1,
		Payload:         &gatewayv1.GatewayFrame_Hello{Hello: hello},
	}
}

// receiveReplacementWelcome waits for the replacement stream's Welcome, bounded
// by the handshake timeout. first carries the received frame even on protocol
// errors so callers can echo its control sequence.
func (s *Supervisor) receiveReplacementWelcome(
	ctx context.Context,
	cancel context.CancelFunc,
	stream Stream,
) (*gatewayv1.ControlWelcome, receiveResult, error) {
	received := make(chan receiveResult, 1)
	go receive(stream, received)
	var first receiveResult
	select {
	case <-ctx.Done():
		return nil, first, ctx.Err()
	case <-s.cfg.Clock.After(s.cfg.HandshakeTimeout):
		return nil, first, errors.New("control supervisor: replacement welcome timeout")
	case first = <-received:
	}
	if first.err != nil {
		return nil, first, fmt.Errorf("receive replacement welcome: %w", first.err)
	}
	welcome, err := validateWelcome(first.frame)
	if err != nil {
		return nil, first, err
	}
	return welcome, first, nil
}

// probeReplacementHeartbeat proves the replacement stream can carry an
// acknowledged heartbeat within the advertised lease.
func (s *Supervisor) probeReplacementHeartbeat(
	probeCtx context.Context,
	cancel context.CancelFunc,
	stream Stream,
	welcome *gatewayv1.ControlWelcome,
	first receiveResult,
	runtime RuntimeSnapshot,
) error {
	heartbeatInterval := time.Duration(welcome.HeartbeatIntervalMs) * time.Millisecond
	leaseTimeout := time.Duration(welcome.LeaseTimeoutMs) * time.Millisecond
	intervalOutOfBounds := heartbeatInterval < s.cfg.MinHeartbeat || heartbeatInterval > s.cfg.MaxHeartbeat
	if intervalOutOfBounds || leaseTimeout <= heartbeatInterval {
		return fmt.Errorf("%w: invalid replacement timing", ErrProtocol)
	}
	heartbeat := &gatewayv1.GatewayFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        2,
		Payload: &gatewayv1.GatewayFrame_Heartbeat{Heartbeat: newGatewayHeartbeat(
			welcome.ConnectionEpoch, first.frame.Sequence, runtime, s.cfg.Clock.Now(),
		)},
	}
	budget := leaseTimeout - heartbeatInterval
	if err := s.sendWithin(probeCtx, cancel, stream, heartbeat, budget); err != nil {
		return fmt.Errorf("send replacement heartbeat: %w", err)
	}
	return s.awaitReplacementAck(probeCtx, cancel, stream, welcome, first, leaseTimeout)
}

func (s *Supervisor) awaitReplacementAck(
	probeCtx context.Context,
	cancel context.CancelFunc,
	stream Stream,
	welcome *gatewayv1.ControlWelcome,
	first receiveResult,
	leaseTimeout time.Duration,
) error {
	acked := make(chan receiveResult, 1)
	go receive(stream, acked)
	select {
	case <-probeCtx.Done():
		return probeCtx.Err()
	case <-s.cfg.Clock.After(leaseTimeout):
		return errors.New("control supervisor: replacement heartbeat acknowledgement timeout")
	case result := <-acked:
		return validateReplacementAck(result, first, welcome, s.cfg.Clock.Now())
	}
}

func validateReplacementAck(result, first receiveResult, welcome *gatewayv1.ControlWelcome, now time.Time) error {
	if result.err != nil {
		return fmt.Errorf("receive replacement heartbeat acknowledgement: %w", result.err)
	}
	const gatewaySequence = 2
	if err := validateControl(
		result.frame,
		first.frame.Sequence+1,
		welcome.ConnectionEpoch,
		gatewaySequence,
		now,
	); err != nil {
		return err
	}
	if result.frame.GetHeartbeatAck() == nil {
		return fmt.Errorf("%w: replacement control frame is not a heartbeat acknowledgement", ErrProtocol)
	}
	return nil
}

func (s *Supervisor) WaitForLifecycleChange(
	ctx context.Context,
	epoch uint64,
	desired gatewayv1.LifecycleDirectiveAction,
) (Status, error) {
	for {
		s.mu.RLock()
		superseded := s.status.Connected &&
			(s.status.ConnectionEpoch != epoch || s.status.DesiredLifecycle != desired)
		if superseded {
			status := copyStatus(s.status)
			s.mu.RUnlock()
			return status, nil
		}
		changed := s.changed
		s.mu.RUnlock()
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-changed:
		}
	}
}

// WaitForDirective waits for a lifecycle directive whose control sequence is
// greater than afterSequence. Welcome desired lifecycle is deliberately not a
// directive and is therefore never returned here.
func (s *Supervisor) WaitForDirective(ctx context.Context, afterSequence uint64) (LifecycleDirective, error) {
	for {
		s.mu.RLock()
		directive := s.status.Directive
		available := s.status.Connected && directive != nil && directive.Sequence > afterSequence
		if available {
			value := *directive
			s.mu.RUnlock()
			return value, nil
		}
		changed := s.changed
		s.mu.RUnlock()
		select {
		case <-ctx.Done():
			return LifecycleDirective{}, ctx.Err()
		case <-changed:
		}
	}
}

// ReportLifecycle sends one report for the exact directive supplied by
// WaitForDirective. It never substitutes a newer directive after reconnect.
func (s *Supervisor) ReportLifecycle(
	ctx context.Context,
	directive LifecycleDirective,
	state gatewayv1.GatewayRuntimeState,
	failure gatewayv1.LifecycleFailure,
) error {
	if err := validateLifecycleReport(directive, state, failure); err != nil {
		return err
	}
	request := lifecycleReportRequest{directive: directive, state: state, failure: failure, result: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.lifecycleReports <- request:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-request.result:
		return err
	}
}

func (s *Supervisor) WaitForStatusChange(ctx context.Context, previous Status) (Status, error) {
	for {
		s.mu.RLock()
		current := copyStatus(s.status)
		stateChanged := current.Ready != previous.Ready ||
			current.Connected != previous.Connected ||
			current.ConnectionEpoch != previous.ConnectionEpoch ||
			current.DesiredLifecycle != previous.DesiredLifecycle
		if stateChanged {
			s.mu.RUnlock()
			return current, nil
		}
		changed := s.changed
		s.mu.RUnlock()
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-changed:
		}
	}
}

// Run reconnects until ctx is cancelled. Cancellation is a clean shutdown.
func (s *Supervisor) Run(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return errors.New("control supervisor: Run called more than once")
	}
	var attempt uint
	for {
		err := s.runStream(ctx)
		stable := s.Status().Stable
		s.disconnected(err)
		if ctx.Err() != nil {
			return nil
		}
		if terminal(err) {
			return err
		}
		if stable {
			attempt = 0
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.cfg.Clock.After(s.cfg.Backoff.Delay(attempt)):
		}
		attempt++
	}
}

// ReportNow requests an immediate heartbeat using the latest Runtime snapshot.
func (s *Supervisor) ReportNow() {
	select {
	case s.report <- struct{}{}:
	default:
	}
}

// Flush sends the latest runtime snapshot immediately and waits for its durable
// heartbeat acknowledgement.
func (s *Supervisor) Flush(ctx context.Context) error {
	state := s.cfg.Runtime.Snapshot().State
	initial := s.Status()
	lastReportEpoch := initial.ConnectionEpoch
	s.ReportNow()
	for {
		s.mu.RLock()
		confirmed := s.status.Connected && s.status.AcknowledgedRuntime == state
		fresh := s.status.ConnectionEpoch != initial.ConnectionEpoch ||
			s.status.LastControlSequence > initial.LastControlSequence
		if confirmed && fresh {
			s.mu.RUnlock()
			return nil
		}
		currentEpoch, connected := s.status.ConnectionEpoch, s.status.Connected
		changed := s.changed
		s.mu.RUnlock()
		if connected && currentEpoch != lastReportEpoch {
			lastReportEpoch = currentEpoch
			s.ReportNow()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// WaitForRuntimeAck waits until the API durably acknowledges a heartbeat that
// carried state. It does not itself mutate the runtime source.
func (s *Supervisor) WaitForRuntimeAck(ctx context.Context, state gatewayv1.GatewayRuntimeState) error {
	for {
		s.mu.RLock()
		if s.status.Connected && s.status.AcknowledgedRuntime == state {
			s.mu.RUnlock()
			return nil
		}
		changed := s.changed
		s.mu.RUnlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (s *Supervisor) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyStatus(s.status)
}

// controlStream holds the mutable state of one established control-stream
// session. All of its methods run on the supervisor's Run goroutine.
type controlStream struct {
	sup    *Supervisor
	ctx    context.Context
	cancel context.CancelFunc
	stream Stream
	recv   chan receiveResult

	welcome           *gatewayv1.ControlWelcome
	heartbeatInterval time.Duration
	leaseTimeout      time.Duration

	nextGatewaySequence uint64
	lastGatewaySequence uint64
	lastControlSequence uint64
	runtime             RuntimeSnapshot

	heartbeat          <-chan time.Time
	lease              <-chan time.Time
	acknowledgedCycles uint32
	pendingReport      bool
	eventInFlight      bool
	expectedEventAck   uint64
}

func (s *Supervisor) runStream(ctx context.Context) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := s.opener.Open(streamCtx)
	if err != nil {
		return fmt.Errorf("open control stream: %w", err)
	}
	session, err := s.openSession(ctx, streamCtx, cancel, stream)
	if err != nil || session == nil {
		return err // nil session with nil error: shutdown requested mid-handshake
	}
	return session.loop(ctx)
}

// openSession performs the authenticated handshake on a freshly opened stream
// and returns the established session. A nil session with a nil error means
// ctx was cancelled while waiting for the Welcome.
func (s *Supervisor) openSession(
	parent context.Context,
	streamCtx context.Context,
	cancel context.CancelFunc,
	stream Stream,
) (*controlStream, error) {
	runtime := s.cfg.Runtime.Snapshot()
	if !validRuntimeState(runtime.State) {
		return nil, fmt.Errorf("%w: invalid runtime state", ErrProtocol)
	}
	if err := s.sendHandshake(streamCtx, cancel, stream, s.newHelloFrame(runtime)); err != nil {
		return nil, fmt.Errorf("send hello: %w", err)
	}
	welcomeFrame, err := s.receiveWelcome(parent, stream)
	if welcomeFrame == nil && err == nil {
		return nil, nil // clean shutdown requested mid-handshake
	}
	if err != nil {
		cancel()
		return nil, err
	}
	welcome, err := validateWelcome(welcomeFrame)
	if err != nil {
		return nil, err
	}
	heartbeatInterval := time.Duration(welcome.HeartbeatIntervalMs) * time.Millisecond
	if heartbeatInterval < s.cfg.MinHeartbeat || heartbeatInterval > s.cfg.MaxHeartbeat {
		return nil, fmt.Errorf("%w: invalid heartbeat interval", ErrProtocol)
	}
	leaseTimeout := time.Duration(welcome.LeaseTimeoutMs) * time.Millisecond
	if leaseTimeout <= heartbeatInterval {
		return nil, fmt.Errorf("%w: invalid lease timeout", ErrProtocol)
	}
	s.connected(welcome, runtime.State)

	session := &controlStream{
		sup:                 s,
		ctx:                 streamCtx,
		cancel:              cancel,
		stream:              stream,
		recv:                make(chan receiveResult, 1),
		welcome:             welcome,
		heartbeatInterval:   heartbeatInterval,
		leaseTimeout:        leaseTimeout,
		nextGatewaySequence: 2,
		lastGatewaySequence: 1,
		lastControlSequence: welcomeFrame.Sequence,
		runtime:             runtime,
	}
	go receive(stream, session.recv)
	// Register the interval timer unconditionally before the immediate-journal
	// override, preserving the historical Clock.After call sequence: Clock
	// implementations observe every registration, and the supervisor's test
	// clock depends on it.
	session.heartbeat = s.cfg.Clock.After(heartbeatInterval)
	if s.cfg.EventJournal != nil {
		session.heartbeat = s.cfg.Clock.After(0)
	}
	session.lease = s.cfg.Clock.After(leaseTimeout)
	return session, nil
}

// receiveWelcome waits for the first control frame on a stream, bounded by the
// handshake timeout. A nil frame with a nil error means parent was cancelled.
func (s *Supervisor) receiveWelcome(parent context.Context, stream Stream) (*gatewayv1.ControlFrame, error) {
	handshakeRecv := make(chan receiveResult, 1)
	go receive(stream, handshakeRecv)
	var result receiveResult
	select {
	case <-parent.Done():
		return nil, nil
	case <-s.cfg.Clock.After(s.cfg.HandshakeTimeout):
		return nil, errors.New("control supervisor: welcome timeout")
	case result = <-handshakeRecv:
	}
	if result.err != nil {
		return nil, fmt.Errorf("receive welcome: %w", result.err)
	}
	return result.frame, nil
}

// loop pumps frames and timers for one established session until ctx is
// cancelled or the stream fails.
func (l *controlStream) loop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-l.lease:
			return errors.New("control supervisor: control lease expired")
		case result := <-l.recv:
			if err := l.handleFrame(result); err != nil {
				return err
			}
			l.lease = l.sup.cfg.Clock.After(l.leaseTimeout)
			go receive(l.stream, l.recv)
		case <-l.heartbeat:
			if err := l.sendHeartbeat(); err != nil {
				return err
			}
		case <-l.sup.report:
			if l.heartbeat == nil {
				l.pendingReport = true
				continue
			}
			if err := l.sendHeartbeat(); err != nil {
				return err
			}
		case request := <-l.sup.lifecycleReports:
			if !l.sup.matchesActiveDirective(request.directive) {
				request.result <- ErrDirectiveSuperseded
				continue
			}
			if err := l.sendLifecycleReport(request); err != nil {
				return err
			}
		}
	}
}

func (l *controlStream) handleFrame(result receiveResult) error {
	if result.err != nil {
		if errors.Is(result.err, io.EOF) {
			return errors.New("control supervisor: stream closed")
		}
		return fmt.Errorf("receive control frame: %w", result.err)
	}
	err := validateControl(
		result.frame,
		l.lastControlSequence+1,
		l.welcome.ConnectionEpoch,
		l.lastGatewaySequence,
		l.now(),
	)
	if err != nil {
		return err
	}
	l.lastControlSequence = result.frame.Sequence
	switch {
	case result.frame.GetEventAck() != nil:
		return l.handleEventAck(result.frame.GetEventAck())
	case result.frame.GetLifecycleDirective() != nil:
		directive := result.frame.GetLifecycleDirective()
		l.sup.directiveReceived(lifecycleDirectiveValue(directive, result.frame.Sequence))
		return nil
	case result.frame.GetDesiredStateSnapshot() != nil:
		return l.applyDesiredState(result.frame.GetDesiredStateSnapshot())
	default:
		l.acknowledgeHeartbeat()
		return nil
	}
}

func (l *controlStream) handleEventAck(ack *gatewayv1.GatewayEventAck) error {
	if l.sup.cfg.EventJournal == nil {
		return fmt.Errorf("%w: unexpected event acknowledgement", ErrProtocol)
	}
	mismatchedBatch := !l.eventInFlight || ack.AcknowledgedJournalSequence != l.expectedEventAck
	if mismatchedBatch {
		return fmt.Errorf("%w: event acknowledgement does not match in-flight batch", ErrProtocol)
	}
	if err := l.sup.cfg.EventJournal.AckEvents(l.ctx, ack.AcknowledgedJournalSequence); err != nil {
		return fmt.Errorf("ack journal events: %w", err)
	}
	l.eventInFlight = false
	l.expectedEventAck = 0
	return l.sendEventBatch()
}

func (l *controlStream) applyDesiredState(snapshot *gatewayv1.DesiredStateSnapshot) error {
	if l.sup.cfg.DesiredState == nil {
		return fmt.Errorf("%w: received desired state without applier", ErrProtocol)
	}
	if err := validateDesiredState(snapshot); err != nil {
		return err
	}
	report, err := l.sup.cfg.DesiredState.ApplyDesiredState(l.ctx, l.welcome.ConnectionEpoch, snapshot)
	if err != nil {
		return fmt.Errorf("apply desired state: %w", err)
	}
	reportValid := report != nil &&
		report.ConnectionEpoch == l.welcome.ConnectionEpoch &&
		report.ProcessedRevision == snapshot.Revision
	if !reportValid {
		return fmt.Errorf("%w: invalid desired-state report", ErrProtocol)
	}
	ack := &gatewayv1.GatewayFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        l.nextGatewaySequence,
		Payload:         &gatewayv1.GatewayFrame_DesiredStateReport{DesiredStateReport: report},
	}
	if err := l.send(ack); err != nil {
		return fmt.Errorf("send desired state acknowledgement: %w", err)
	}
	l.nextGatewaySequence++
	l.sup.desiredStateApplied(snapshot.Revision, desiredStateHealthy(report))
	return nil
}

// acknowledgeHeartbeat records an acknowledged cycle and re-arms the heartbeat
// timer, honoring any report request that arrived while an acknowledgement was
// outstanding.
func (l *controlStream) acknowledgeHeartbeat() {
	l.acknowledgedCycles++
	stable := l.acknowledgedCycles >= 2
	l.sup.heartbeatAcknowledged(l.lastControlSequence, l.runtime.State, stable)
	if l.pendingReport {
		l.pendingReport = false
		l.heartbeat = l.sup.cfg.Clock.After(0)
		return
	}
	l.heartbeat = l.sup.cfg.Clock.After(l.heartbeatInterval)
}

func (l *controlStream) sendLifecycleReport(request lifecycleReportRequest) error {
	report := &gatewayv1.GatewayFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        l.nextGatewaySequence,
		Payload: &gatewayv1.GatewayFrame_LifecycleReport{LifecycleReport: &gatewayv1.GatewayLifecycleReport{
			ConnectionEpoch: request.directive.ConnectionEpoch,
			DirectiveId:     request.directive.ID,
			State:           request.state,
			Failure:         request.failure,
		}},
	}
	sendErr := l.send(report)
	if sendErr != nil {
		request.result <- fmt.Errorf("send lifecycle report: %w", sendErr)
		return fmt.Errorf("send lifecycle report: %w", sendErr)
	}
	l.nextGatewaySequence++
	l.sup.directiveReported(request.directive)
	request.result <- nil
	return nil
}

func (l *controlStream) sendHeartbeat() error {
	l.runtime = l.sup.cfg.Runtime.Snapshot()
	if !validRuntimeState(l.runtime.State) {
		return fmt.Errorf("%w: invalid runtime state", ErrProtocol)
	}
	frame := l.newHeartbeatFrame()
	if pressure, ok := l.journalPressure(); ok {
		telemetry := frame.GetHeartbeat()
		telemetry.JournalState = pressure.State
		telemetry.JournalEntries = pressure.Entries
		telemetry.JournalBytes = pressure.Bytes
	}
	if err := l.send(frame); err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}
	l.lastGatewaySequence = l.nextGatewaySequence
	l.nextGatewaySequence++
	if err := l.sendEventBatch(); err != nil {
		return err
	}
	l.heartbeat = nil
	return nil
}

// journalPressure reads optional journal telemetry for the heartbeat. Unreadable
// telemetry is omitted, never fabricated; the API treats UNKNOWN as "no report
// this cycle".
func (l *controlStream) journalPressure() (JournalPressure, bool) {
	if l.sup.cfg.JournalMetrics == nil {
		return JournalPressure{}, false
	}
	pressure, err := l.sup.cfg.JournalMetrics(l.ctx)
	if err != nil {
		return JournalPressure{}, false
	}
	return pressure, true
}

func (l *controlStream) newHeartbeatFrame() *gatewayv1.GatewayFrame {
	return &gatewayv1.GatewayFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        l.nextGatewaySequence,
		Payload: &gatewayv1.GatewayFrame_Heartbeat{Heartbeat: newGatewayHeartbeat(
			l.welcome.ConnectionEpoch,
			l.lastControlSequence,
			l.runtime,
			l.now(),
		)},
	}
}

func newGatewayHeartbeat(
	connectionEpoch uint64,
	lastControlSequence uint64,
	snapshot RuntimeSnapshot,
	now time.Time,
) *gatewayv1.GatewayHeartbeat {
	return &gatewayv1.GatewayHeartbeat{
		ConnectionEpoch:     connectionEpoch,
		LastControlSequence: lastControlSequence,
		SentAtUnixMs:        now.UnixMilli(),
		SessionCount:        snapshot.SessionCount,
		RuntimeState:        snapshot.State,
	}
}

func (l *controlStream) sendEventBatch() error {
	if l.sup.cfg.EventJournal == nil || l.eventInFlight {
		return nil
	}
	batch, err := l.sup.cfg.EventJournal.NextEventBatch(l.ctx, l.welcome.ConnectionEpoch)
	if err != nil {
		return fmt.Errorf("read journal batch: %w", err)
	}
	if len(batch.Events) == 0 {
		return nil
	}
	frame := &gatewayv1.GatewayFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        l.nextGatewaySequence,
		Payload:         &gatewayv1.GatewayFrame_EventBatch{EventBatch: batch},
	}
	if err := l.send(frame); err != nil {
		return fmt.Errorf("send journal batch: %w", err)
	}
	l.nextGatewaySequence++
	l.eventInFlight = true
	l.expectedEventAck = batch.Events[len(batch.Events)-1].JournalSequence
	return nil
}

// send writes one gateway frame within the session's remaining lease budget.
func (l *controlStream) send(frame *gatewayv1.GatewayFrame) error {
	return l.sup.sendWithin(l.ctx, l.cancel, l.stream, frame, l.leaseTimeout-l.heartbeatInterval)
}

func (l *controlStream) now() time.Time {
	return l.sup.cfg.Clock.Now()
}

func (s *Supervisor) sendHandshake(
	ctx context.Context,
	cancel context.CancelFunc,
	stream Stream,
	frame *gatewayv1.GatewayFrame,
) error {
	result := make(chan error, 1)
	go func() { result <- stream.Send(frame) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.cfg.Clock.After(s.cfg.HandshakeTimeout):
		cancel()
		return errors.New("control supervisor: hello timeout")
	case err := <-result:
		return err
	}
}

func (s *Supervisor) sendWithin(
	ctx context.Context,
	cancel context.CancelFunc,
	stream Stream,
	frame *gatewayv1.GatewayFrame,
	timeout time.Duration,
) error {
	result := make(chan error, 1)
	go func() { result <- stream.Send(frame) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.cfg.Clock.After(timeout):
		cancel()
		return errors.New("control supervisor: send timeout")
	case err := <-result:
		return err
	}
}

type receiveResult struct {
	frame *gatewayv1.ControlFrame
	err   error
}

func receive(stream Stream, result chan<- receiveResult) {
	frame, err := stream.Recv()
	result <- receiveResult{frame: frame, err: err}
}

func validateWelcome(frame *gatewayv1.ControlFrame) (*gatewayv1.ControlWelcome, error) {
	if frame == nil || frame.ProtocolVersion != ProtocolVersion || frame.Sequence != 1 {
		return nil, fmt.Errorf("%w: invalid welcome envelope", ErrProtocol)
	}
	welcome := frame.GetWelcome()
	if welcome == nil || welcome.ConnectionId == "" || welcome.ConnectionEpoch == 0 || welcome.ServerTimeUnixMs <= 0 {
		return nil, fmt.Errorf("%w: invalid welcome", ErrProtocol)
	}
	if !validLifecycleAction(welcome.DesiredLifecycle) {
		return nil, fmt.Errorf("%w: invalid desired lifecycle", ErrProtocol)
	}
	return welcome, nil
}

func validateControl(frame *gatewayv1.ControlFrame, sequence, epoch, gatewaySequence uint64, now time.Time) error {
	if frame == nil || frame.ProtocolVersion != ProtocolVersion || frame.Sequence != sequence {
		return fmt.Errorf("%w: invalid control envelope", ErrProtocol)
	}
	if directive := frame.GetLifecycleDirective(); directive != nil {
		return validateDirective(directive, epoch, now)
	}
	if snapshot := frame.GetDesiredStateSnapshot(); snapshot != nil {
		return validateDesiredState(snapshot)
	}
	if ack := frame.GetEventAck(); ack != nil {
		if ack.AcknowledgedJournalSequence == 0 {
			return fmt.Errorf("%w: invalid event acknowledgement", ErrProtocol)
		}
		return nil
	}
	ack := frame.GetHeartbeatAck()
	invalidAck := ack == nil ||
		ack.ConnectionEpoch != epoch ||
		ack.AcknowledgedGatewaySequence != gatewaySequence ||
		ack.ServerTimeUnixMs <= 0 ||
		gatewaySequence <= 1
	if invalidAck {
		return fmt.Errorf("%w: invalid heartbeat acknowledgement", ErrProtocol)
	}
	return nil
}

func validateDesiredState(snapshot *gatewayv1.DesiredStateSnapshot) error {
	if snapshot == nil || snapshot.Revision == 0 {
		return fmt.Errorf("%w: invalid desired state snapshot", ErrProtocol)
	}
	seen := make(map[string]struct{}, len(snapshot.Assignments))
	for _, assignment := range snapshot.Assignments {
		if !validAssignment(assignment) {
			return fmt.Errorf("%w: invalid session assignment", ErrProtocol)
		}
		if _, exists := seen[assignment.SessionId]; exists {
			return fmt.Errorf("%w: duplicate session assignment", ErrProtocol)
		}
		seen[assignment.SessionId] = struct{}{}
	}
	return nil
}

func validAssignment(assignment *gatewayv1.SessionAssignment) bool {
	if assignment == nil {
		return false
	}
	knownAction := assignment.DesiredAction == gatewayv1.SessionDesiredAction_SESSION_DESIRED_ACTION_RUN ||
		assignment.DesiredAction == gatewayv1.SessionDesiredAction_SESSION_DESIRED_ACTION_STOP
	runWithoutDevice := assignment.DesiredAction == gatewayv1.SessionDesiredAction_SESSION_DESIRED_ACTION_RUN &&
		assignment.DeviceJid == nil

	return assignment.SessionId != "" &&
		assignment.OrganizationId != "" &&
		assignment.AssignmentEpoch != 0 &&
		assignment.LeaseExpiresAtUnixMs > 0 &&
		knownAction &&
		!runWithoutDevice
}

func validateDirective(directive *gatewayv1.LifecycleDirective, epoch uint64, now time.Time) error {
	invalid := directive == nil ||
		directive.DirectiveId == "" ||
		directive.ConnectionEpoch != epoch ||
		!validLifecycleAction(directive.Action) ||
		!validDirectiveReason(directive.Reason)
	if invalid {
		return fmt.Errorf("%w: invalid lifecycle directive", ErrProtocol)
	}
	hasDeadline := directive.DrainDeadlineUnixMs != nil
	if directive.Action != gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN {
		if hasDeadline {
			return fmt.Errorf("%w: unexpected drain deadline", ErrProtocol)
		}
		return nil
	}
	expired := hasDeadline && directive.GetDrainDeadlineUnixMs() <= now.UnixMilli()
	if expired {
		return fmt.Errorf("%w: invalid drain deadline", ErrProtocol)
	}
	return nil
}

func validLifecycleAction(action gatewayv1.LifecycleDirectiveAction) bool {
	switch action {
	case gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN,
		gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN,
		gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DISABLE:
		return true
	default:
		return false
	}
}

func validDirectiveReason(reason gatewayv1.LifecycleDirectiveReason) bool {
	switch reason {
	case gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_OPERATOR,
		gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_MAINTENANCE,
		gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_CAPACITY,
		gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_POLICY:
		return true
	default:
		return false
	}
}

func lifecycleDirectiveValue(directive *gatewayv1.LifecycleDirective, sequence uint64) LifecycleDirective {
	value := LifecycleDirective{
		ID:              directive.DirectiveId,
		ConnectionEpoch: directive.ConnectionEpoch,
		Sequence:        sequence,
		Action:          directive.Action,
		Reason:          directive.Reason,
	}
	if directive.DrainDeadlineUnixMs != nil {
		value.DrainDeadline = time.UnixMilli(*directive.DrainDeadlineUnixMs).UTC()
	}
	return value
}

func validateLifecycleReport(
	directive LifecycleDirective,
	state gatewayv1.GatewayRuntimeState,
	failure gatewayv1.LifecycleFailure,
) error {
	invalid := directive.ID == "" ||
		directive.ConnectionEpoch == 0 ||
		directive.Sequence == 0 ||
		!validLifecycleAction(directive.Action) ||
		!validDirectiveReason(directive.Reason) ||
		!validRuntimeState(state)
	if invalid {
		return fmt.Errorf("%w: invalid lifecycle report", ErrProtocol)
	}
	switch failure {
	case gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE,
		gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_BUSY,
		gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_TIMEOUT,
		gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_INTERNAL:
		return nil
	default:
		return fmt.Errorf("%w: invalid lifecycle failure", ErrProtocol)
	}
}

func validRuntimeState(state gatewayv1.GatewayRuntimeState) bool {
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

func validHTTPBaseURL(raw string) bool {
	if len(raw) > 512 || strings.HasSuffix(raw, "/") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Host != "" && parsed.User == nil && parsed.Path == "" &&
		parsed.RawQuery == "" && parsed.Fragment == ""
}

func (s *Supervisor) connected(welcome *gatewayv1.ControlWelcome, runtime gatewayv1.GatewayRuntimeState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = Status{
		Ready:               false,
		Connected:           true,
		ConnectionEpoch:     welcome.ConnectionEpoch,
		LastControlSequence: 1,
		DesiredLifecycle:    welcome.DesiredLifecycle,
	}
	s.signalChangedLocked()
}

func desiredStateHealthy(report *gatewayv1.DesiredStateReport) bool {
	if report.GetKeystoreHealth().GetState() != gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY {
		return false
	}
	for _, result := range report.GetResults() {
		if result.GetStatus() != gatewayv1.ReconciliationResultStatus_RECONCILIATION_RESULT_STATUS_APPLIED {
			return false
		}
	}
	return true
}

func (s *Supervisor) desiredStateApplied(revision uint64, healthy bool) {
	s.mu.Lock()
	s.status.DesiredStateRevision = revision
	s.status.DesiredStateApplied = true
	s.status.DesiredStateHealthy = healthy
	s.signalChangedLocked()
	s.mu.Unlock()
}

func (s *Supervisor) heartbeatAcknowledged(sequence uint64, runtime gatewayv1.GatewayRuntimeState, stable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.ConfirmedHeartbeat = true
	s.status.Stable = stable
	s.status.LastControlSequence = sequence
	s.status.AcknowledgedRuntime = runtime
	runAndReady := s.status.DesiredLifecycle == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN &&
		runtime == gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY &&
		s.status.DesiredStateApplied &&
		s.status.DesiredStateHealthy
	s.status.Ready = runAndReady
	s.signalChangedLocked()
}

func (s *Supervisor) directiveReceived(directive LifecycleDirective) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.LastControlSequence = directive.Sequence
	s.status.DesiredLifecycle = directive.Action
	s.status.Directive = &directive
	s.status.Ready = false
	s.signalChangedLocked()
}

func (s *Supervisor) matchesActiveDirective(directive LifecycleDirective) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	active := s.status.Directive
	return s.status.Connected && active != nil && *active == directive
}

func (s *Supervisor) directiveReported(directive LifecycleDirective) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Directive != nil && *s.status.Directive == directive {
		s.status.Directive = nil
		s.signalChangedLocked()
	}
}

func (s *Supervisor) disconnected(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Ready = false
	s.status.Connected = false
	s.status.ConfirmedHeartbeat = false
	s.status.Stable = false
	s.status.ConnectionEpoch = 0
	s.status.LastControlSequence = 0
	s.status.LastError = err
	s.signalChangedLocked()
}

func (s *Supervisor) signalChangedLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func copyStatus(status Status) Status {
	if status.Directive != nil {
		directive := *status.Directive
		status.Directive = &directive
	}
	return status
}

func terminal(err error) bool {
	if errors.Is(err, ErrProtocol) {
		return true
	}
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument, codes.FailedPrecondition:
		return true
	default:
		return false
	}
}

type realClock struct{}

func (realClock) Now() time.Time                                { return time.Now() }
func (realClock) After(duration time.Duration) <-chan time.Time { return time.After(duration) }

type ExponentialBackoff struct {
	Min, Max time.Duration
	Jitter   float64
}

func (b ExponentialBackoff) Delay(attempt uint) time.Duration {
	if b.Min <= 0 {
		b.Min = 250 * time.Millisecond
	}
	if b.Max < b.Min {
		b.Max = 30 * time.Second
	}
	delay := b.Min
	for range attempt {
		if delay >= b.Max/2 {
			break
		}
		delay *= 2
	}
	if delay > b.Max {
		delay = b.Max
	}
	if b.Jitter <= 0 {
		return delay
	}
	return time.Duration(rand.Float64() * float64(delay))
}

type ConnOpener struct {
	conn grpc.ClientConnInterface
}

type ConnProvider interface{ Conn() *grpc.ClientConn }

type CurrentConnOpener struct{ provider ConnProvider }

func NewConnOpener(conn grpc.ClientConnInterface) (*ConnOpener, error) {
	if conn == nil {
		return nil, errors.New("control supervisor: nil connection")
	}
	return &ConnOpener{conn: conn}, nil
}

func (o *ConnOpener) Open(ctx context.Context) (Stream, error) {
	return gatewayv1.NewGatewayControlServiceClient(o.conn).Connect(ctx)
}

func NewCurrentConnOpener(provider ConnProvider) (*CurrentConnOpener, error) {
	if provider == nil || provider.Conn() == nil {
		return nil, errors.New("control supervisor: nil connection provider")
	}
	return &CurrentConnOpener{provider: provider}, nil
}

func (o *CurrentConnOpener) Open(ctx context.Context) (Stream, error) {
	conn := o.provider.Conn()
	if conn == nil {
		return nil, errors.New("control supervisor: no current connection")
	}
	return gatewayv1.NewGatewayControlServiceClient(conn).Connect(ctx)
}
