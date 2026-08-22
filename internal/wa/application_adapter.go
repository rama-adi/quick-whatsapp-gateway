package wa

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

type sessionStateSource interface {
	ConnectionState(string) (domain.SessionStatus, bool, bool, bool)
}

type engineLiveOps interface {
	SetPresence(context.Context, string, string) error
	SendReadReceiptAt(context.Context, string, string, string, []string, time.Time) error
}

type assignmentFence interface {
	AllowsMutation(string, string, uint64) bool
	OwnsSession(string, string, uint64) bool
}

// sendDispatcher routes a validated request to the live WhatsApp client for
// one session. It is deliberately the rate-limit-free, idempotency-free
// dispatch chokepoint: scheduling and product limits belong to the API.
type sendDispatcher interface {
	Dispatch(ctx context.Context, req domain.SendRequest) (waMessageID string, ts int64, err error)
}

// opDispatcher routes one validated message sub-resource operation to
// whatsmeow, rate-limit-free like sendDispatcher.
type opDispatcher interface {
	DispatchOp(ctx context.Context, req outbound.OpRequest) (outbound.SendResult, error)
}

// commandLedger is the gateway-local durable record of definite command
// outcomes. Lookup returns nil when no terminal result exists.
type commandLedger interface {
	LookupCommand(ctx context.Context, commandID string) (*application.CommandResultRecord, error)
	SaveCommandResult(ctx context.Context, result application.CommandResultRecord) error
}

// ApplicationGatewayAdapter exposes existing in-process WhatsApp operations
// through the transport-independent application boundary.
type ApplicationGatewayAdapter struct {
	gatewayID     string
	sessions      sessionStateSource
	live          engineLiveOps
	now           func() time.Time
	maxFutureSkew time.Duration
	fence         assignmentFence
	dispatch      sendDispatcher
	opDispatch    opDispatcher
	ledger        commandLedger

	// inFlight serializes concurrent identical commands within this process so
	// a duplicate RPC waits for the first execution instead of racing it. The
	// durable ledger covers restarts; this map covers concurrency.
	inFlightMu sync.Mutex
	inFlight   map[string]*inFlightSend
}

type inFlightSend struct {
	done          chan struct{}
	result        application.SendMessageResult
	opWAMessageID string
	err           error
}

type inFlightOp struct {
	done   chan struct{}
	result application.MutationResult
	err    error
}

// DefaultReadReceiptFutureSkew tolerates small clock differences between the
// API and gateway without accepting timestamps far in the future.
const DefaultReadReceiptFutureSkew = 30 * time.Second

// NewApplicationGatewayAdapter constructs the local adapter used by the private
// gRPC server. The supplied fence is checked immediately before live operations.
// Dispatcher and ledger are required for SendMessage and optional otherwise.
func NewApplicationGatewayAdapter(gatewayID string, manager *Manager, fence assignmentFence, dispatcher interface {
	sendDispatcher
	opDispatcher
}, ledger commandLedger) *ApplicationGatewayAdapter {
	return &ApplicationGatewayAdapter{
		gatewayID:     gatewayID,
		sessions:      manager,
		live:          manager.LiveOps(),
		now:           time.Now,
		maxFutureSkew: DefaultReadReceiptFutureSkew,
		fence:         fence,
		dispatch:      dispatcher,
		opDispatch:    dispatcher,
		ledger:        ledger,
		inFlight:      map[string]*inFlightSend{},
	}
}

var _ application.GatewayEngine = (*ApplicationGatewayAdapter)(nil)

func (a *ApplicationGatewayAdapter) GetSessionState(_ context.Context, query application.SessionStateQuery) (application.SessionState, error) {
	if err := a.validateTarget(query.OrganizationID, query.SessionID, query.GatewayID); err != nil {
		return application.SessionState{}, err
	}
	if a.fence != nil && (query.AssignmentEpoch == 0 || !a.fence.OwnsSession(query.OrganizationID, query.SessionID, query.AssignmentEpoch)) {
		return application.SessionState{}, domain.ErrConflict("session assignment is not live")
	}
	status, connected, loggedIn, found := a.sessions.ConnectionState(query.SessionID)
	if !found {
		return application.SessionState{}, domain.ErrNotFound("session not found")
	}
	return application.SessionState{
		OrganizationID: query.OrganizationID,
		SessionID:      query.SessionID,
		GatewayID:      query.GatewayID,
		Status:         status,
		Connected:      connected,
		LoggedIn:       loggedIn,
	}, nil
}

func (a *ApplicationGatewayAdapter) SetAccountPresence(ctx context.Context, command application.SetPresenceCommand) (application.MutationResult, error) {
	if err := a.validateMutation(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID); err != nil {
		return application.MutationResult{}, err
	}
	if command.AssignmentEpoch == 0 {
		return application.MutationResult{}, domain.ErrValidation("assignment_epoch must be at least 1")
	}
	if a.fence != nil && !a.fence.AllowsMutation(command.OrganizationID, command.SessionID, command.AssignmentEpoch) {
		return application.MutationResult{}, domain.ErrConflict("session assignment epoch is stale or lease expired")
	}
	if command.State != application.AccountPresenceOnline && command.State != application.AccountPresenceOffline {
		return application.MutationResult{}, domain.ErrValidation("presence state must be online or offline")
	}
	if err := a.live.SetPresence(ctx, command.SessionID, string(command.State)); err != nil {
		return application.MutationResult{}, err
	}
	return mutationResult(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch), nil
}

func (a *ApplicationGatewayAdapter) MarkRead(ctx context.Context, command application.MarkReadCommand) (application.MutationResult, error) {
	if err := a.validateMutation(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID); err != nil {
		return application.MutationResult{}, err
	}
	if command.AssignmentEpoch == 0 {
		return application.MutationResult{}, domain.ErrValidation("assignment_epoch must be at least 1")
	}
	if a.fence != nil && !a.fence.AllowsMutation(command.OrganizationID, command.SessionID, command.AssignmentEpoch) {
		return application.MutationResult{}, domain.ErrConflict("session assignment epoch is stale or lease expired")
	}
	if command.ChatJID == "" || len(command.MessageIDs) == 0 || command.ReadAt.IsZero() {
		return application.MutationResult{}, domain.ErrValidation("chat_jid, message_ids, and read_at are required")
	}
	chatJID, err := parseJID(command.ChatJID)
	if err != nil || !validReceiptChatJID(chatJID) {
		return application.MutationResult{}, domain.ErrValidation("chat_jid is invalid")
	}
	if chatJID.Server == types.GroupServer && command.SenderJID == "" {
		return application.MutationResult{}, domain.ErrValidation("sender_jid is required for group read receipts")
	}
	if command.SenderJID != "" {
		if jid, err := parseJID(command.SenderJID); err != nil || !validReceiptSenderJID(jid) {
			return application.MutationResult{}, domain.ErrValidation("sender_jid is invalid")
		}
	}
	for _, messageID := range command.MessageIDs {
		if messageID == "" {
			return application.MutationResult{}, domain.ErrValidation("message_ids must not contain empty values")
		}
	}
	if command.ReadAt.After(a.now().Add(a.maxFutureSkew)) {
		return application.MutationResult{}, domain.ErrValidation("read_at exceeds allowed future clock skew")
	}
	if err := a.live.SendReadReceiptAt(ctx, command.SessionID, command.ChatJID, command.SenderJID, command.MessageIDs, command.ReadAt); err != nil {
		return application.MutationResult{}, err
	}
	return mutationResult(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch), nil
}

// SendMessage executes one durable send command behind the assignment fence.
// A repeated CommandID returns the stored terminal result without re-dispatch;
// only definite outcomes (sent, or a pre-dispatch validation failure) are
// recorded. Transient and post-dispatch unknowns stay unrecorded so the API's
// retry re-issues the command and either replays or reconciles.
func (a *ApplicationGatewayAdapter) SendMessage(ctx context.Context, command application.SendCommand) (application.SendMessageResult, error) {
	if err := a.validateMutation(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID); err != nil {
		return application.SendMessageResult{}, err
	}
	if command.AssignmentEpoch == 0 {
		return application.SendMessageResult{}, domain.ErrValidation("assignment_epoch must be at least 1")
	}

	// Replay precedes the fence: a recorded result is evidence of an execution
	// that already happened under an earlier valid assignment.
	record, err := a.lookupCommand(ctx, command.CommandID)
	if err != nil {
		return application.SendMessageResult{}, err
	}
	if record != nil {
		return replayedSendResult(command, record)
	}

	if a.fence != nil && !a.fence.AllowsMutation(command.OrganizationID, command.SessionID, command.AssignmentEpoch) {
		return application.SendMessageResult{}, domain.ErrConflict("session assignment epoch is stale or lease expired")
	}

	result, err := a.executeSend(ctx, command)
	if err != nil {
		return application.SendMessageResult{}, err
	}
	return result, nil
}

func (a *ApplicationGatewayAdapter) lookupCommand(ctx context.Context, commandID string) (*application.CommandResultRecord, error) {
	if a.ledger == nil {
		return nil, domain.ErrValidation("gateway send ledger is not configured")
	}
	record, err := a.ledger.LookupCommand(ctx, commandID)
	if err != nil {
		return nil, fmt.Errorf("lookup send command result: %w", err)
	}
	return record, nil
}

func (a *ApplicationGatewayAdapter) executeSend(ctx context.Context, command application.SendCommand) (result application.SendMessageResult, err error) {
	flight, follower := a.joinInFlight(command.CommandID)
	if follower {
		select {
		case <-flight.done:
			return flight.result, flight.err
		case <-ctx.Done():
			return application.SendMessageResult{}, ctx.Err()
		}
	}
	// Publish the outcome to concurrent duplicates before removing the entry:
	// the ledger write happens inside, and followers must observe the terminal
	// result rather than racing their own execution.
	defer func() {
		flight.result, flight.err = result, err
		close(flight.done)
		a.leaveInFlight(command.CommandID)
	}()

	waMessageID, ts, dispatchErr := a.dispatch.Dispatch(outbound.WithSessionID(ctx, command.SessionID), command.Payload)
	if dispatchErr != nil {
		// Only deterministic pre-dispatch rejections are terminal failures.
		// Everything else stays unrecorded for retry/reconciliation.
		var apiErr *domain.APIError
		if errors.As(dispatchErr, &apiErr) && apiErr.Code == domain.CodeValidationError {
			saveErr := a.saveCommand(ctx, application.CommandResultRecord{
				CommandID: command.CommandID, SessionID: command.SessionID,
				Status: application.CommandFailed, Error: dispatchErr.Error(), UpdatedAt: a.now().UTC(),
			})
			if saveErr != nil {
				return application.SendMessageResult{}, saveErr
			}
		}
		return application.SendMessageResult{}, dispatchErr
	}

	sentAt := a.now()
	if ts > 0 {
		sentAt = time.UnixMilli(ts).UTC()
	}
	result = application.SendMessageResult{
		MutationResult: mutationResult(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch),
		WAMessageID:    waMessageID,
		SentAt:         sentAt,
	}
	// Write-ahead response: the ledger row exists before this RPC answers, so
	// a lost response can be resolved by replay instead of re-dispatch.
	if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
		CommandID: command.CommandID, SessionID: command.SessionID,
		Status: application.CommandSent, WAMessageID: waMessageID, UpdatedAt: sentAt,
	}); saveErr != nil {
		return application.SendMessageResult{}, saveErr
	}
	return result, nil
}

// joinInFlight returns (flight, true) for a follower that must wait, or
// (its own flight, false) for the leader that will execute.
func (a *ApplicationGatewayAdapter) joinInFlight(commandID string) (*inFlightSend, bool) {
	a.inFlightMu.Lock()
	defer a.inFlightMu.Unlock()
	if flight, ok := a.inFlight[commandID]; ok {
		return flight, true
	}
	flight := &inFlightSend{done: make(chan struct{})}
	a.inFlight[commandID] = flight
	return flight, false
}

func (a *ApplicationGatewayAdapter) leaveInFlight(commandID string) {
	a.inFlightMu.Lock()
	defer a.inFlightMu.Unlock()
	delete(a.inFlight, commandID)
}

func (a *ApplicationGatewayAdapter) saveCommand(ctx context.Context, record application.CommandResultRecord) error {
	if err := a.ledger.SaveCommandResult(ctx, record); err != nil {
		return fmt.Errorf("save send command result %s: %w", record.CommandID, err)
	}
	return nil
}

// replayedSendResult reconstructs the original outcome from one stored record.
// The stored UpdatedAt is the best available execution timestamp; the fence and
// epoch in the replayed result echo the current request's routing metadata.
func replayedSendResult(command application.SendCommand, record *application.CommandResultRecord) (application.SendMessageResult, error) {
	switch record.Status {
	case application.CommandSent:
		return application.SendMessageResult{
			MutationResult: mutationResult(record.CommandID, command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch),
			WAMessageID:    record.WAMessageID,
			SentAt:         record.UpdatedAt,
		}, nil
	case application.CommandFailed:
		return application.SendMessageResult{}, domain.ErrValidation("send previously failed: " + record.Error)
	default:
		return application.SendMessageResult{}, fmt.Errorf("unknown stored command status %q", record.Status)
	}
}

func validReceiptChatJID(jid types.JID) bool {
	if jid.User == "" {
		return false
	}
	switch jid.Server {
	case types.DefaultUserServer, types.LegacyUserServer, types.GroupServer, types.HiddenUserServer:
		return true
	default:
		return false
	}
}

func validReceiptSenderJID(jid types.JID) bool {
	if jid.User == "" {
		return false
	}
	return jid.Server == types.DefaultUserServer || jid.Server == types.LegacyUserServer || jid.Server == types.HiddenUserServer
}

func (a *ApplicationGatewayAdapter) validateMutation(commandID, organizationID, sessionID, gatewayID string) error {
	if commandID == "" {
		return domain.ErrValidation("command_id is required")
	}
	return a.validateTarget(organizationID, sessionID, gatewayID)
}

func (a *ApplicationGatewayAdapter) validateTarget(organizationID, sessionID, gatewayID string) error {
	if organizationID == "" || sessionID == "" || gatewayID == "" {
		return domain.ErrValidation("organization_id, session_id, and gateway_id are required")
	}
	if gatewayID != a.gatewayID {
		return domain.ErrNotFound("gateway target does not match this gateway")
	}
	return nil
}

func mutationResult(commandID, organizationID, sessionID, gatewayID string, assignmentEpoch uint64) application.MutationResult {
	return application.MutationResult{
		CommandID:       commandID,
		OrganizationID:  organizationID,
		SessionID:       sessionID,
		GatewayID:       gatewayID,
		AssignmentEpoch: assignmentEpoch,
	}
}

// ExecuteOp runs one message sub-resource command behind the assignment fence
// with the same ledger semantics as SendMessage.
func (a *ApplicationGatewayAdapter) ExecuteOp(ctx context.Context, command application.MessageOpCommand) (application.MessageOpResult, error) {
	if err := a.validateMutation(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID); err != nil {
		return application.MessageOpResult{}, err
	}
	if command.AssignmentEpoch == 0 {
		return application.MessageOpResult{}, domain.ErrValidation("assignment_epoch must be at least 1")
	}

	record, err := a.lookupCommand(ctx, command.CommandID)
	if err != nil {
		return application.MessageOpResult{}, err
	}
	if record != nil {
		return replayedMutation(command, record)
	}
	if a.fence != nil && !a.fence.AllowsMutation(command.OrganizationID, command.SessionID, command.AssignmentEpoch) {
		return application.MessageOpResult{}, domain.ErrConflict("session assignment epoch is stale or lease expired")
	}

	return a.executeOp(ctx, command)
}

// executeOp shares the send singleflight: a concurrent duplicate of either
// command kind waits for the leader and reads its published outcome.
func (a *ApplicationGatewayAdapter) executeOp(ctx context.Context, command application.MessageOpCommand) (opResult application.MessageOpResult, err error) {
	flight, follower := a.joinInFlight(command.CommandID)
	if follower {
		select {
		case <-flight.done:
			return application.MessageOpResult{MutationResult: flight.result.MutationResult, WAMessageID: flight.opWAMessageID}, flight.err
		case <-ctx.Done():
			return application.MessageOpResult{}, ctx.Err()
		}
	}
	defer func() {
		flight.result = application.SendMessageResult{MutationResult: opResult.MutationResult}
		flight.opWAMessageID = opResult.WAMessageID
		flight.err = err
		close(flight.done)
		a.leaveInFlight(command.CommandID)
	}()

	waResult, opErr := a.opDispatch.DispatchOp(outbound.WithSessionID(ctx, command.SessionID), outbound.OpRequest{
		Op:      outbound.MessageOp(command.Op),
		Chat:    command.ChatJID,
		Sender:  command.SenderJID,
		MsgID:   command.MessageID,
		Emoji:   command.Emoji,
		NewText: command.NewText,
		Options: command.Options,
		To:      command.ToJID,
	})
	if opErr != nil {
		var apiErr *domain.APIError
		if errors.As(opErr, &apiErr) && apiErr.Code == domain.CodeValidationError {
			saveErr := a.saveCommand(ctx, application.CommandResultRecord{
				CommandID: command.CommandID, SessionID: command.SessionID,
				Status: application.CommandFailed, Error: opErr.Error(), UpdatedAt: a.now().UTC(),
			})
			if saveErr != nil {
				return application.MessageOpResult{}, saveErr
			}
		}
		return application.MessageOpResult{}, opErr
	}

	sentAt := a.now()
	if waResult.Timestamp > 0 {
		sentAt = time.UnixMilli(waResult.Timestamp).UTC()
	}
	out := application.MessageOpResult{
		MutationResult: mutationResult(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch),
		WAMessageID:    waResult.WAMessageID,
	}
	if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
		CommandID: command.CommandID, SessionID: command.SessionID,
		Status: application.CommandSent, WAMessageID: waResult.WAMessageID, UpdatedAt: sentAt,
	}); saveErr != nil {
		return application.MessageOpResult{}, saveErr
	}
	return out, nil
}

// replayedMutation reconstructs a stored outcome for message-op commands. A
// stored failure replays as validation; a stored success returns routing
// metadata only (ops carry no additional response payload).
func replayedMutation(command application.MessageOpCommand, record *application.CommandResultRecord) (application.MessageOpResult, error) {
	switch record.Status {
	case application.CommandSent:
		return application.MessageOpResult{
			MutationResult: mutationResult(record.CommandID, command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch),
			WAMessageID:    record.WAMessageID,
		}, nil
	case application.CommandFailed:
		return application.MessageOpResult{}, domain.ErrValidation("op previously failed: " + record.Error)
	default:
		return application.MessageOpResult{}, fmt.Errorf("unknown stored command status %q", record.Status)
	}
}
