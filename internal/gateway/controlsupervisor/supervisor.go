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
	StartedAt        time.Time
	Runtime          RuntimeSource
	Clock            Clock
	Backoff          Backoff
	MinHeartbeat     time.Duration
	MaxHeartbeat     time.Duration
	HandshakeTimeout time.Duration
}

type Status struct {
	Ready               bool
	Connected           bool
	ConfirmedHeartbeat  bool
	Stable              bool
	ConnectionEpoch     uint64
	LastControlSequence uint64
	AcknowledgedRuntime gatewayv1.GatewayRuntimeState
	DesiredLifecycle    gatewayv1.LifecycleDirectiveAction
	Directive           *LifecycleDirective
	LastError           error
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
		cfg: cfg, opener: opener,
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

func (s *Supervisor) WaitForLifecycleChange(ctx context.Context, epoch uint64, desired gatewayv1.LifecycleDirectiveAction) (Status, error) {
	for {
		s.mu.RLock()
		if s.status.Connected && (s.status.ConnectionEpoch != epoch || s.status.DesiredLifecycle != desired) {
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
		if s.status.Connected && directive != nil && directive.Sequence > afterSequence {
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
func (s *Supervisor) ReportLifecycle(ctx context.Context, directive LifecycleDirective, state gatewayv1.GatewayRuntimeState, failure gatewayv1.LifecycleFailure) error {
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
		if current.Ready != previous.Ready || current.Connected != previous.Connected ||
			current.ConnectionEpoch != previous.ConnectionEpoch || current.DesiredLifecycle != previous.DesiredLifecycle {
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
		if s.status.Connected && s.status.AcknowledgedRuntime == state &&
			(s.status.ConnectionEpoch != initial.ConnectionEpoch || s.status.LastControlSequence > initial.LastControlSequence) {
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

func (s *Supervisor) runStream(ctx context.Context) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := s.opener.Open(streamCtx)
	if err != nil {
		return fmt.Errorf("open control stream: %w", err)
	}
	runtime := s.cfg.Runtime.Snapshot()
	if !validRuntimeState(runtime.State) {
		return fmt.Errorf("%w: invalid runtime state", ErrProtocol)
	}
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
	helloFrame := &gatewayv1.GatewayFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        1,
		Payload:         &gatewayv1.GatewayFrame_Hello{Hello: hello},
	}
	if err = s.sendHandshake(streamCtx, cancel, stream, helloFrame); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	handshakeRecv := make(chan receiveResult, 1)
	go receive(stream, handshakeRecv)
	var welcomeFrame *gatewayv1.ControlFrame
	select {
	case <-ctx.Done():
		return nil
	case <-s.cfg.Clock.After(s.cfg.HandshakeTimeout):
		cancel()
		return errors.New("control supervisor: welcome timeout")
	case result := <-handshakeRecv:
		if result.err != nil {
			return fmt.Errorf("receive welcome: %w", result.err)
		}
		welcomeFrame = result.frame
	}
	welcome, err := validateWelcome(welcomeFrame)
	if err != nil {
		return err
	}
	heartbeatInterval := time.Duration(welcome.HeartbeatIntervalMs) * time.Millisecond
	if heartbeatInterval < s.cfg.MinHeartbeat || heartbeatInterval > s.cfg.MaxHeartbeat {
		return fmt.Errorf("%w: invalid heartbeat interval", ErrProtocol)
	}
	leaseTimeout := time.Duration(welcome.LeaseTimeoutMs) * time.Millisecond
	if leaseTimeout <= heartbeatInterval {
		return fmt.Errorf("%w: invalid lease timeout", ErrProtocol)
	}
	s.connected(welcome, runtime.State)

	recv := make(chan receiveResult, 1)
	go receive(stream, recv)
	nextGatewaySequence := uint64(2)
	lastGatewaySequence := uint64(1)
	lastControlSequence := welcomeFrame.Sequence
	heartbeat := s.cfg.Clock.After(heartbeatInterval)
	lease := s.cfg.Clock.After(leaseTimeout)
	acknowledgedCycles := uint32(0)
	pendingReport := false
	sendHeartbeat := func() error {
		runtime = s.cfg.Runtime.Snapshot()
		if !validRuntimeState(runtime.State) {
			return fmt.Errorf("%w: invalid runtime state", ErrProtocol)
		}
		heartbeatFrame := &gatewayv1.GatewayHeartbeat{
			ConnectionEpoch:     welcome.ConnectionEpoch,
			LastControlSequence: lastControlSequence,
			SentAtUnixMs:        s.cfg.Clock.Now().UnixMilli(),
			SessionCount:        runtime.SessionCount,
			RuntimeState:        runtime.State,
		}
		heartbeatEnvelope := &gatewayv1.GatewayFrame{
			ProtocolVersion: ProtocolVersion,
			Sequence:        nextGatewaySequence,
			Payload:         &gatewayv1.GatewayFrame_Heartbeat{Heartbeat: heartbeatFrame},
		}
		if sendErr := s.sendWithin(streamCtx, cancel, stream, heartbeatEnvelope, leaseTimeout-heartbeatInterval); sendErr != nil {
			return fmt.Errorf("send heartbeat: %w", sendErr)
		}
		lastGatewaySequence = nextGatewaySequence
		nextGatewaySequence++
		heartbeat = nil
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-lease:
			return errors.New("control supervisor: control lease expired")
		case result := <-recv:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return errors.New("control supervisor: stream closed")
				}
				return fmt.Errorf("receive control frame: %w", result.err)
			}
			if err = validateControl(result.frame, lastControlSequence+1, welcome.ConnectionEpoch, lastGatewaySequence, s.cfg.Clock.Now()); err != nil {
				return err
			}
			lastControlSequence = result.frame.Sequence
			if directive := result.frame.GetLifecycleDirective(); directive != nil {
				s.directiveReceived(lifecycleDirectiveValue(directive, result.frame.Sequence))
			} else {
				acknowledgedCycles++
				s.heartbeatAcknowledged(lastControlSequence, runtime.State, acknowledgedCycles >= 2)
				if pendingReport {
					heartbeat = s.cfg.Clock.After(0)
					pendingReport = false
				} else {
					heartbeat = s.cfg.Clock.After(heartbeatInterval)
				}
			}
			lease = s.cfg.Clock.After(leaseTimeout)
			go receive(stream, recv)
		case <-heartbeat:
			if err = sendHeartbeat(); err != nil {
				return err
			}
		case <-s.report:
			if heartbeat == nil {
				pendingReport = true
				continue
			}
			if err = sendHeartbeat(); err != nil {
				return err
			}
		case request := <-s.lifecycleReports:
			if !s.matchesActiveDirective(request.directive) {
				request.result <- ErrDirectiveSuperseded
				continue
			}
			report := &gatewayv1.GatewayFrame{
				ProtocolVersion: ProtocolVersion,
				Sequence:        nextGatewaySequence,
				Payload: &gatewayv1.GatewayFrame_LifecycleReport{LifecycleReport: &gatewayv1.GatewayLifecycleReport{
					ConnectionEpoch: request.directive.ConnectionEpoch,
					DirectiveId:     request.directive.ID,
					State:           request.state,
					Failure:         request.failure,
				}},
			}
			if sendErr := s.sendWithin(streamCtx, cancel, stream, report, leaseTimeout-heartbeatInterval); sendErr != nil {
				request.result <- fmt.Errorf("send lifecycle report: %w", sendErr)
				return fmt.Errorf("send lifecycle report: %w", sendErr)
			}
			nextGatewaySequence++
			s.directiveReported(request.directive)
			request.result <- nil
		}
	}
}

func (s *Supervisor) sendHandshake(ctx context.Context, cancel context.CancelFunc, stream Stream, frame *gatewayv1.GatewayFrame) error {
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

func (s *Supervisor) sendWithin(ctx context.Context, cancel context.CancelFunc, stream Stream, frame *gatewayv1.GatewayFrame, timeout time.Duration) error {
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
	switch welcome.DesiredLifecycle {
	case gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN,
		gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN,
		gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DISABLE:
	default:
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
	ack := frame.GetHeartbeatAck()
	if ack == nil || ack.ConnectionEpoch != epoch || ack.AcknowledgedGatewaySequence != gatewaySequence ||
		ack.ServerTimeUnixMs <= 0 || gatewaySequence <= 1 {
		return fmt.Errorf("%w: invalid heartbeat acknowledgement", ErrProtocol)
	}
	return nil
}

func validateDirective(directive *gatewayv1.LifecycleDirective, epoch uint64, now time.Time) error {
	if directive == nil || directive.DirectiveId == "" || directive.ConnectionEpoch != epoch || !validLifecycleAction(directive.Action) || !validDirectiveReason(directive.Reason) {
		return fmt.Errorf("%w: invalid lifecycle directive", ErrProtocol)
	}
	if directive.Action == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN {
		if directive.DrainDeadlineUnixMs != nil && directive.GetDrainDeadlineUnixMs() <= now.UnixMilli() {
			return fmt.Errorf("%w: invalid drain deadline", ErrProtocol)
		}
	} else if directive.DrainDeadlineUnixMs != nil {
		return fmt.Errorf("%w: unexpected drain deadline", ErrProtocol)
	}
	return nil
}

func validLifecycleAction(action gatewayv1.LifecycleDirectiveAction) bool {
	return action == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN || action == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN || action == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DISABLE
}

func validDirectiveReason(reason gatewayv1.LifecycleDirectiveReason) bool {
	return reason == gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_OPERATOR || reason == gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_MAINTENANCE || reason == gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_CAPACITY || reason == gatewayv1.LifecycleDirectiveReason_LIFECYCLE_DIRECTIVE_REASON_POLICY
}

func lifecycleDirectiveValue(directive *gatewayv1.LifecycleDirective, sequence uint64) LifecycleDirective {
	value := LifecycleDirective{ID: directive.DirectiveId, ConnectionEpoch: directive.ConnectionEpoch, Sequence: sequence, Action: directive.Action, Reason: directive.Reason}
	if directive.DrainDeadlineUnixMs != nil {
		value.DrainDeadline = time.UnixMilli(*directive.DrainDeadlineUnixMs).UTC()
	}
	return value
}

func validateLifecycleReport(directive LifecycleDirective, state gatewayv1.GatewayRuntimeState, failure gatewayv1.LifecycleFailure) error {
	if directive.ID == "" || directive.ConnectionEpoch == 0 || directive.Sequence == 0 || !validLifecycleAction(directive.Action) || !validDirectiveReason(directive.Reason) || !validRuntimeState(state) {
		return fmt.Errorf("%w: invalid lifecycle report", ErrProtocol)
	}
	if failure != gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE && failure != gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_BUSY && failure != gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_TIMEOUT && failure != gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_INTERNAL {
		return fmt.Errorf("%w: invalid lifecycle failure", ErrProtocol)
	}
	return nil
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

func (s *Supervisor) heartbeatAcknowledged(sequence uint64, runtime gatewayv1.GatewayRuntimeState, stable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.ConfirmedHeartbeat = true
	s.status.Stable = stable
	s.status.LastControlSequence = sequence
	s.status.AcknowledgedRuntime = runtime
	s.status.Ready = s.status.DesiredLifecycle == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN &&
		runtime == gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY
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
	for i := uint(0); i < attempt && delay < b.Max/2; i++ {
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

func NewConnOpener(conn grpc.ClientConnInterface) (*ConnOpener, error) {
	if conn == nil {
		return nil, errors.New("control supervisor: nil connection")
	}
	return &ConnOpener{conn: conn}, nil
}

func (o *ConnOpener) Open(ctx context.Context) (Stream, error) {
	return gatewayv1.NewGatewayControlServiceClient(o.conn).Connect(ctx)
}
