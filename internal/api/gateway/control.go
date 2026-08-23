// Package gateway implements the API-owned gateway control-plane application
// service. Persistence is deliberately expressed in transport-neutral values.
package gateway

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const ProtocolVersion uint32 = 1

const (
	DefaultHelloTimeout       = 10 * time.Second
	DefaultHeartbeatInterval  = 5 * time.Second
	DefaultLeaseTimeout       = 15 * time.Second
	DefaultDisconnectTimeout  = 5 * time.Second
	MaxInstanceIDBytes        = 128
	MaxSoftwareVersionBytes   = 128
	MaxGRPCEndpointBytes      = 512
	MaxHTTPBaseURLBytes       = 512
	MaxGatewayCapabilities    = 16
	MaxGatewayEventBatch      = 256
	MaxGatewayEventBatchBytes = 1 << 20
	maxUnixMillis             = int64(253402300799999)
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
	JournalState        gatewayv1.GatewayJournalState
	JournalEntries      uint64
	JournalBytes        uint64
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

type DesiredSession struct {
	SessionID, OrganizationID, DeviceJID string
	DesiredAction                        gatewayv1.SessionDesiredAction
	AssignmentEpoch, ConfigRevision      uint64
	AutoRead, PresenceTyping             bool
	RatePerMin, RatePerHour              uint32
	LeaseExpiresAt                       time.Time
}

// DesiredState is a complete, revisioned replacement for a gateway's local
// assignment set. Every supplied assignment lease must be in the future.
type DesiredState struct {
	Revision    uint64
	Assignments []DesiredSession
}
type ReconciliationReport struct {
	Revision                 uint64
	KeystoreState            string
	KeystoreBytes, CheckedAt *int64
	LocalDevices             []string
	Results                  []ReconciliationResult
}
type ReconciliationResult struct {
	SessionID         *string
	AssignmentEpoch   uint64
	DeviceJID, Status string
}
type GatewayEvent struct {
	JournalSequence, ConnectionEpoch, AssignmentEpoch uint64
	EventID, SessionID, OrganizationID, Type          string
	OccurredAt                                        time.Time
	Payload                                           []byte
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
	DesiredState(context.Context, string, uint64, uint64, time.Time) (DesiredState, error)
	PersistDesiredStateReport(context.Context, string, uint64, ReconciliationReport) error
	IngestEvents(context.Context, string, uint64, []GatewayEvent) error
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
	invalidAllocation := connection.ID == "" || connection.Epoch == 0 ||
		connection.HeartbeatInterval <= 0 || connection.LeaseTimeout <= 0 ||
		!knownLifecycleAction(connection.DesiredLifecycle)
	if invalidAllocation {
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
	outboundSequence := welcome.Sequence
	if outboundSequence, err = s.sendDesiredState(stream, gatewayID, connection, outboundSequence); err != nil {
		return err
	}

	leaseTimer := time.NewTimer(connection.LeaseTimeout)
	defer leaseTimer.Stop()
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
			ack := payload.Heartbeat.LastControlSequence
			ackAhead := ack > outboundSequence
			ackBehind := ack < lastAcknowledgedControlSequence
			if ackAhead || ackBehind {
				return status.Error(codes.FailedPrecondition, "gateway control acknowledgement mismatch")
			}
			lastAcknowledgedControlSequence = ack
			desired, heartbeatErr := s.Store.Heartbeat(
				stream.Context(), gatewayID, connection.Epoch, heartbeatValue(payload.Heartbeat),
			)
			err = heartbeatErr
			if err == nil {
				if err = validateDesiredLifecycle(desired); err != nil {
					return err
				}
				lifecycleRegressed := desired.Revision < lastIssuedDesiredRevision ||
					(desired.Revision == lastIssuedDesiredRevision && desired.Action != lastIssuedDesiredAction)
				if lifecycleRegressed {
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
				if outboundSequence, err = s.sendDesiredState(
					stream, gatewayID,
					Connection{Epoch: connection.Epoch, LeaseTimeout: connection.LeaseTimeout, DesiredRevision: desired.Revision},
					outboundSequence,
				); err != nil {
					return err
				}
			}
		case *gatewayv1.GatewayFrame_DesiredStateReport:
			if err = validateDesiredStateReport(payload.DesiredStateReport, connection.Epoch); err != nil {
				return err
			}
			err = s.Store.PersistDesiredStateReport(
				stream.Context(), gatewayID, connection.Epoch, reconciliationReport(payload.DesiredStateReport),
			)
		case *gatewayv1.GatewayFrame_EventBatch:
			events, validationErr := eventBatch(payload.EventBatch, gatewayID, connection.Epoch)
			if validationErr != nil {
				return validationErr
			}
			if err = s.Store.IngestEvents(stream.Context(), gatewayID, connection.Epoch, events); err == nil {
				outboundSequence++
				ack := &gatewayv1.ControlFrame{
					ProtocolVersion: ProtocolVersion,
					Sequence:        outboundSequence,
					Payload: &gatewayv1.ControlFrame_EventAck{EventAck: &gatewayv1.GatewayEventAck{
						AcknowledgedJournalSequence: events[len(events)-1].JournalSequence,
					}},
				}
				if err = stream.Send(ack); err != nil {
					// A lost acknowledgement is a transport failure, not a
					// persistence failure: map it like every other send so the
					// gateway's journal replay is classified as retryable.
					return sendStatus(err)
				}
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

func eventBatch(batch *gatewayv1.GatewayEventBatch, gatewayID string, epoch uint64) ([]GatewayEvent, error) {
	if batch == nil || len(batch.Events) == 0 || len(batch.Events) > MaxGatewayEventBatch {
		return nil, status.Error(codes.InvalidArgument, "invalid gateway event batch")
	}
	out := make([]GatewayEvent, 0, len(batch.Events))
	var last uint64
	if proto.Size(batch) > MaxGatewayEventBatchBytes {
		return nil, status.Error(codes.InvalidArgument, "gateway event batch too large")
	}
	for _, v := range batch.Events {
		if v == nil {
			return nil, status.Error(codes.FailedPrecondition, "invalid gateway event")
		}
		unknownGateway := v.GatewayId != gatewayID
		badSequence := v.JournalSequence == 0 || v.JournalSequence <= last
		wrongEpoch := v.ConnectionEpoch != epoch
		unassigned := v.AssignmentEpoch == 0
		missingIdentity := v.EventId == "" || v.SessionId == "" || v.OrganizationId == "" || v.EventType == ""
		badTimestamp := v.OccurredAtUnixMs <= 0
		if unknownGateway || badSequence || wrongEpoch || unassigned || missingIdentity || badTimestamp {
			return nil, status.Error(codes.FailedPrecondition, "invalid gateway event")
		}
		last = v.JournalSequence
		out = append(out, GatewayEvent{
			JournalSequence: v.JournalSequence,
			ConnectionEpoch: v.ConnectionEpoch,
			AssignmentEpoch: v.AssignmentEpoch,
			EventID:         v.EventId,
			SessionID:       v.SessionId,
			OrganizationID:  v.OrganizationId,
			Type:            v.EventType,
			OccurredAt:      time.UnixMilli(v.OccurredAtUnixMs).UTC(),
			Payload:         append([]byte(nil), v.Payload...),
		})
	}
	return out, nil
}

func (s *Server) sendDesiredState(
	stream gatewayv1.GatewayControlService_ConnectServer,
	gatewayID string,
	connection Connection,
	sequence uint64,
) (uint64, error) {
	leaseExpiresAt := s.clock().Add(connection.LeaseTimeout)
	desired, err := s.Store.DesiredState(stream.Context(), gatewayID, connection.Epoch, connection.DesiredRevision, leaseExpiresAt)
	if err != nil {
		return sequence, storeStatus(err)
	}
	if desired.Revision != connection.DesiredRevision || !validDesiredState(desired, leaseExpiresAt) {
		return sequence, status.Error(codes.Internal, "invalid gateway desired state")
	}
	assignments := make([]*gatewayv1.SessionAssignment, 0, len(desired.Assignments))
	for _, assignment := range desired.Assignments {
		item := &gatewayv1.SessionAssignment{
			SessionId:            assignment.SessionID,
			OrganizationId:       assignment.OrganizationID,
			AssignmentEpoch:      assignment.AssignmentEpoch,
			DesiredAction:        assignment.DesiredAction,
			LeaseExpiresAtUnixMs: assignment.LeaseExpiresAt.UnixMilli(),
			Config: &gatewayv1.SessionConfig{
				Revision:       assignment.ConfigRevision,
				AutoRead:       assignment.AutoRead,
				PresenceTyping: assignment.PresenceTyping,
				RatePerMin:     assignment.RatePerMin,
				RatePerHour:    assignment.RatePerHour,
			},
		}
		if assignment.DeviceJID != "" {
			item.DeviceJid = &assignment.DeviceJID
		}
		assignments = append(assignments, item)
	}
	sequence++
	snapshot := &gatewayv1.ControlFrame{
		ProtocolVersion: ProtocolVersion,
		Sequence:        sequence,
		Payload: &gatewayv1.ControlFrame_DesiredStateSnapshot{DesiredStateSnapshot: &gatewayv1.DesiredStateSnapshot{
			Revision:    desired.Revision,
			Assignments: assignments,
		}},
	}
	if err := stream.Send(snapshot); err != nil {
		return sequence, sendStatus(err)
	}
	return sequence, nil
}

func validDesiredState(desired DesiredState, minimumLease time.Time) bool {
	if desired.Revision == 0 {
		return false
	}
	for _, assignment := range desired.Assignments {
		missingIdentity := assignment.SessionID == "" || assignment.OrganizationID == ""
		unknownAction := assignment.DesiredAction == gatewayv1.SessionDesiredAction_SESSION_DESIRED_ACTION_UNKNOWN
		staleRevision := assignment.ConfigRevision != desired.Revision
		expiredLease := !assignment.LeaseExpiresAt.After(minimumLease.Add(-time.Millisecond))
		if missingIdentity || assignment.AssignmentEpoch == 0 || unknownAction || staleRevision || expiredLease {
			return false
		}
	}
	return true
}

func validateDesiredStateReport(report *gatewayv1.DesiredStateReport, epoch uint64) error {
	if report == nil {
		return status.Error(codes.FailedPrecondition, "invalid desired state report")
	}
	badEpoch := report.ConnectionEpoch == 0 || report.ConnectionEpoch != epoch
	keystoreUnreported := report.KeystoreHealth == nil ||
		report.KeystoreHealth.State == gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_UNKNOWN
	if badEpoch || keystoreUnreported {
		return status.Error(codes.FailedPrecondition, "invalid desired state report")
	}
	return nil
}

func reconciliationReport(report *gatewayv1.DesiredStateReport) ReconciliationReport {
	keystoreState := strings.TrimPrefix(strings.ToLower(report.KeystoreHealth.State.String()), "keystore_health_state_")
	out := ReconciliationReport{Revision: report.ProcessedRevision, KeystoreState: keystoreState}
	if report.KeystoreHealth.ByteSize != nil {
		v := *report.KeystoreHealth.ByteSize
		out.KeystoreBytes = &v
	}
	if report.KeystoreHealth.LastCheckedAtUnixMs != nil {
		v := *report.KeystoreHealth.LastCheckedAtUnixMs
		out.CheckedAt = &v
	}
	for _, device := range report.LocalDevices {
		out.LocalDevices = append(out.LocalDevices, device.DeviceJid)
	}
	for _, r := range report.Results {
		resultStatus := strings.TrimPrefix(strings.ToLower(r.Status.String()), "reconciliation_result_status_")
		out.Results = append(out.Results, ReconciliationResult{
			SessionID:       r.SessionId,
			AssignmentEpoch: r.AssignmentEpoch,
			DeviceJID:       r.DeviceJid,
			Status:          resultStatus,
		})
	}
	return out
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

func receiveBefore(
	ctx context.Context,
	received <-chan receiveResult,
	deadline <-chan time.Time,
	timeoutMessage string,
) (*gatewayv1.GatewayFrame, error) {
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
	if heartbeat == nil {
		return status.Error(codes.InvalidArgument, "invalid gateway heartbeat")
	}
	badClock := heartbeat.SentAtUnixMs <= 0 || heartbeat.SentAtUnixMs > maxUnixMillis
	if heartbeat.ConnectionEpoch == 0 || badClock || !knownRuntimeState(heartbeat.RuntimeState) {
		return status.Error(codes.InvalidArgument, "invalid gateway heartbeat")
	}
	if !knownJournalState(heartbeat.JournalState) {
		return status.Error(codes.InvalidArgument, "invalid gateway journal state")
	}
	journalUnreported := heartbeat.JournalEntries != 0 || heartbeat.JournalBytes != 0
	if heartbeat.JournalState == gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_UNKNOWN && journalUnreported {
		return status.Error(codes.InvalidArgument, "gateway journal telemetry requires a journal state")
	}
	return nil
}

func knownJournalState(state gatewayv1.GatewayJournalState) bool {
	switch state {
	case gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_UNKNOWN,
		gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_HEALTHY,
		gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_DEGRADED,
		gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_PAUSED,
		gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_CRITICAL:
		return true
	default:
		return false
	}
}

type issuedDirective struct {
	id     string
	action gatewayv1.LifecycleDirectiveAction
}

func directiveID(connectionID string, revision uint64) string {
	return connectionID + ":" + strconv.FormatUint(revision, 10)
}

func validateDesiredLifecycle(desired DesiredLifecycle) error {
	if !knownLifecycleAction(desired.Action) {
		return status.Error(codes.Internal, "invalid desired lifecycle")
	}
	return nil
}

func validateLifecycleReport(
	report *gatewayv1.GatewayLifecycleReport,
	epoch uint64,
	directive issuedDirective,
) error {
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
	return Hello{
		InstanceID:      v.InstanceId,
		SoftwareVersion: v.SoftwareVersion,
		GRPCEndpoint:    endpoint,
		HTTPBaseURL:     httpBaseURL,
		Capabilities:    append([]gatewayv1.GatewayCapability(nil), v.Capabilities...),
		StartedAt:       time.UnixMilli(v.StartedAtUnixMs).UTC(),
		SessionCount:    v.SessionCount,
		RuntimeState:    v.RuntimeState,
	}
}

func validateHTTPBaseURL(raw string) error {
	if len(raw) == 0 || len(raw) > MaxHTTPBaseURLBytes {
		return status.Error(codes.InvalidArgument, "invalid gateway HTTP base URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid gateway HTTP base URL")
	}
	httpScheme := u.Scheme == "http" || u.Scheme == "https"
	bareOrigin := u.Hostname() != "" && u.User == nil &&
		u.RawQuery == "" && u.Fragment == "" && u.RawPath == "" && u.Path == ""
	if !httpScheme || !bareOrigin || u.String() != raw {
		return status.Error(codes.InvalidArgument, "invalid gateway HTTP base URL")
	}
	return nil
}

func heartbeatValue(v *gatewayv1.GatewayHeartbeat) Heartbeat {
	return Heartbeat{
		LastControlSequence: v.LastControlSequence,
		SentAt:              time.UnixMilli(v.SentAtUnixMs).UTC(),
		SessionCount:        v.SessionCount,
		RuntimeState:        v.RuntimeState,
		JournalState:        v.JournalState,
		JournalEntries:      v.JournalEntries,
		JournalBytes:        v.JournalBytes,
	}
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
