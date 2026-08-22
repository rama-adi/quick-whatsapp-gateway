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
	IsOnWhatsApp(context.Context, string, []string) ([]domain.OnWhatsApp, error)
	ProfilePicture(context.Context, string, string) (domain.ProfilePicture, error)
	About(context.Context, string, string) (string, error)
	SetBlocked(context.Context, string, string, bool) error
	CreateGroup(context.Context, string, string, []string) (domain.GroupInfo, error)
	UpdateParticipants(context.Context, string, string, []string, domain.GroupParticipantAction) error
	UpdateSettings(context.Context, string, string, domain.GroupSettings) error
	GetInviteLink(context.Context, string, string, bool) (string, error)
	JoinWithLink(context.Context, string, string) (string, error)
	Leave(context.Context, string, string) error
	GetPresence(context.Context, string, string) (domain.PresenceStatus, error)
	SetChatPresence(context.Context, string, string, string) error
	BackfillSessionData(context.Context, string) (domain.BackfillSnapshot, error)
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
	controller    sessionController
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
	groupInfo     application.GroupInfoResult
	err           error
}

type inFlightOp struct {
	done   chan struct{}
	result application.MutationResult
	err    error
}

// groupInfoResult converts a domain group view to the transport-independent
// result type.
func groupInfoResult(info domain.GroupInfo) application.GroupInfoResult {
	return application.GroupInfoResult{
		GroupJID:     info.GroupJID,
		Subject:      info.Subject,
		Description:  info.Description,
		OwnerJID:     info.OwnerJID,
		Participants: int32(info.Participants),
		IsAnnounce:   info.IsAnnounce,
		IsLocked:     info.IsLocked,
	}
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
		controller:    manager,
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

// ---- Live resource slices (Increment 7) ----
//
// Reads (contact lookups, picture/about, invite links, chat-presence
// subscription, backfill) execute directly behind the assignment fence and
// never touch the result ledger: repeating them is safe by construction.
// Mutations (block/unblock, group create/settings/participants/leave) reuse the
// send pipeline's replay-then-fence-then-singleflight shape so a repeated
// command_id returns the stored terminal outcome instead of re-executing.

func (a *ApplicationGatewayAdapter) fenceQuery(query application.SessionStateQuery) error {
	if err := a.validateTarget(query.OrganizationID, query.SessionID, query.GatewayID); err != nil {
		return err
	}
	if a.fence != nil && (query.AssignmentEpoch == 0 || !a.fence.OwnsSession(query.OrganizationID, query.SessionID, query.AssignmentEpoch)) {
		return domain.ErrConflict("session assignment is not live")
	}
	return nil
}

// LookupContact checks phone numbers against WhatsApp. Read-only: no ledger.
func (a *ApplicationGatewayAdapter) LookupContact(ctx context.Context, command application.LookupContactCommand) ([]application.ContactLookup, error) {
	query := sessionQueryFrom(command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch)
	if err := a.fenceQuery(query); err != nil {
		return nil, err
	}
	if len(command.Phones) == 0 {
		return nil, domain.ErrValidation("phones must not be empty")
	}
	responses, err := a.live.IsOnWhatsApp(ctx, command.SessionID, command.Phones)
	if err != nil {
		return nil, err
	}
	out := make([]application.ContactLookup, 0, len(responses))
	for _, r := range responses {
		out = append(out, application.ContactLookup{Query: r.Query, JID: r.JID, IsIn: r.IsIn})
	}
	return out, nil
}

// GetContactPicture fetches a contact's profile picture. Read-only: no ledger.
func (a *ApplicationGatewayAdapter) GetContactPicture(ctx context.Context, query application.SessionStateQuery, jid string) (domain.ProfilePicture, error) {
	if err := a.fenceQuery(query); err != nil {
		return domain.ProfilePicture{}, err
	}
	if jid == "" {
		return domain.ProfilePicture{}, domain.ErrValidation("jid is required")
	}
	return a.live.ProfilePicture(ctx, query.SessionID, jid)
}

// GetContactAbout fetches a contact's status text. Read-only: no ledger.
func (a *ApplicationGatewayAdapter) GetContactAbout(ctx context.Context, query application.SessionStateQuery, jid string) (string, error) {
	if err := a.fenceQuery(query); err != nil {
		return "", err
	}
	if jid == "" {
		return "", domain.ErrValidation("jid is required")
	}
	return a.live.About(ctx, query.SessionID, jid)
}

// SetBlocked applies one block/unblock command behind the assignment fence with
// full ledger semantics. Only CommandSent is recorded — the blocklist mutation
// carries no WhatsApp message id — and stored failures replay as validation.
func (a *ApplicationGatewayAdapter) SetBlocked(ctx context.Context, command application.ContactJIDCommand) (application.MutationOnlyResult, error) {
	if err := a.validateMutation(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID); err != nil {
		return application.MutationOnlyResult{}, err
	}
	if command.AssignmentEpoch == 0 {
		return application.MutationOnlyResult{}, domain.ErrValidation("assignment_epoch must be at least 1")
	}
	record, err := a.lookupCommand(ctx, command.CommandID)
	if err != nil {
		return application.MutationOnlyResult{}, err
	}
	if record != nil {
		return replayedBlockingMutation(record)
	}
	if a.fence != nil && !a.fence.AllowsMutation(command.OrganizationID, command.SessionID, command.AssignmentEpoch) {
		return application.MutationOnlyResult{}, domain.ErrConflict("session assignment epoch is stale or lease expired")
	}
	flight, follower := a.joinInFlight(command.CommandID)
	if follower {
		select {
		case <-flight.done:
			return application.MutationOnlyResult{MutationResult: flight.result.MutationResult}, flight.err
		case <-ctx.Done():
			return application.MutationOnlyResult{}, ctx.Err()
		}
	}
	var (
		result application.MutationOnlyResult
		err2   error
	)
	defer func() {
		flight.result = application.SendMessageResult{MutationResult: result.MutationResult}
		flight.err = err2
		close(flight.done)
		a.leaveInFlight(command.CommandID)
	}()

	err2 = a.live.SetBlocked(ctx, command.SessionID, command.JID, command.Blocked)
	if err2 != nil {
		return application.MutationOnlyResult{}, err2
	}
	result = application.MutationOnlyResult{MutationResult: mutationResult(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch)}
	if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
		CommandID: command.CommandID, SessionID: command.SessionID,
		Status: application.CommandSent, UpdatedAt: a.now().UTC(),
	}); saveErr != nil {
		return application.MutationOnlyResult{}, saveErr
	}
	return result, nil
}

// replayedBlockingMutation reconstructs a stored blocklist outcome. Successes
// carry routing metadata only; failures replay as validation errors.
func replayedBlockingMutation(record *application.CommandResultRecord) (application.MutationOnlyResult, error) {
	switch record.Status {
	case application.CommandSent:
		return application.MutationOnlyResult{MutationResult: application.MutationResult{
			CommandID: record.CommandID,
		}}, nil
	case application.CommandFailed:
		return application.MutationOnlyResult{}, domain.ErrValidation("op previously failed: " + record.Error)
	default:
		return application.MutationOnlyResult{}, fmt.Errorf("unknown stored command status %q", record.Status)
	}
}

// MutateGroup executes one durable group command behind the assignment fence
// with the same ledger semantics as SendMessage. The returned result carries
// raw live metadata; the API persists its own projections from it.
func (a *ApplicationGatewayAdapter) MutateGroup(ctx context.Context, command application.GroupMutationCommand) (application.GroupCreateResult, error) {
	if err := a.validateMutation(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID); err != nil {
		return application.GroupCreateResult{}, err
	}
	if command.AssignmentEpoch == 0 {
		return application.GroupCreateResult{}, domain.ErrValidation("assignment_epoch must be at least 1")
	}
	record, err := a.lookupCommand(ctx, command.CommandID)
	if err != nil {
		return application.GroupCreateResult{}, err
	}
	if record != nil {
		return replayedGroupMutation(command, record)
	}
	if a.fence != nil && !a.fence.AllowsMutation(command.OrganizationID, command.SessionID, command.AssignmentEpoch) {
		return application.GroupCreateResult{}, domain.ErrConflict("session assignment epoch is stale or lease expired")
	}
	flight, follower := a.joinInFlight(command.CommandID)
	if follower {
		select {
		case <-flight.done:
			return application.GroupCreateResult{MutationOnlyResult: application.MutationOnlyResult{MutationResult: flight.result.MutationResult}, CreatedGroup: flight.groupInfo}, flight.err
		case <-ctx.Done():
			return application.GroupCreateResult{}, ctx.Err()
		}
	}
	var (
		result application.GroupCreateResult
		err2   error
	)
	defer func() {
		flight.result = application.SendMessageResult{MutationResult: result.MutationResult}
		flight.groupInfo = result.CreatedGroup
		flight.err = err2
		close(flight.done)
		a.leaveInFlight(command.CommandID)
	}()

	info, groupErr := a.executeGroupMutation(ctx, command)
	if groupErr != nil {
		// Deterministic rejections are recorded as terminal failures; everything
		// else stays unrecorded for retry/reconciliation.
		var apiErr *domain.APIError
		if errors.As(groupErr, &apiErr) && apiErr.Code == domain.CodeValidationError {
			if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
				CommandID: command.CommandID, SessionID: command.SessionID,
				Status: application.CommandFailed, Error: groupErr.Error(), UpdatedAt: a.now().UTC(),
			}); saveErr != nil {
				return application.GroupCreateResult{}, saveErr
			}
		}
		return application.GroupCreateResult{}, groupErr
	}
	sentAt := a.now().UTC()
	result = application.GroupCreateResult{
		MutationOnlyResult: application.MutationOnlyResult{MutationResult: mutationResult(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch)},
		CreatedGroup:       groupInfoResult(info),
	}
	if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
		CommandID: command.CommandID, SessionID: command.SessionID,
		Status: application.CommandSent, WAMessageID: info.GroupJID, UpdatedAt: sentAt,
	}); saveErr != nil {
		return application.GroupCreateResult{}, saveErr
	}
	return result, nil
}

// executeGroupMutation routes one validated group command to the live client.
// JID/action validation happens here so deterministic rejections can be
// recorded as terminal failures.
func (a *ApplicationGatewayAdapter) executeGroupMutation(ctx context.Context, command application.GroupMutationCommand) (domain.GroupInfo, error) {
	switch command.Kind {
	case application.GroupOpCreate:
		if len(command.Participants) == 0 {
			return domain.GroupInfo{}, domain.ErrValidation("at least one participant is required")
		}
		return a.live.CreateGroup(ctx, command.SessionID, command.Name, command.Participants)
	case application.GroupOpUpdateSettings:
		if command.Settings.Subject == nil && command.Settings.Description == nil && command.Settings.Announce == nil && command.Settings.Locked == nil {
			return domain.GroupInfo{}, domain.ErrValidation("no group settings to update")
		}
		return domain.GroupInfo{}, a.live.UpdateSettings(ctx, command.SessionID, command.GroupJID, application.ToDomainGroupSettings(command.Settings))
	case application.GroupOpUpdateParticipants:
		if len(command.Participants) == 0 {
			return domain.GroupInfo{}, domain.ErrValidation("at least one participant is required")
		}
		action := domain.GroupParticipantAction(command.Action)
		switch action {
		case domain.GroupActionAdd, domain.GroupActionRemove, domain.GroupActionPromote, domain.GroupActionDemote:
		default:
			return domain.GroupInfo{}, domain.ErrValidation("invalid participant action")
		}
		return domain.GroupInfo{}, a.live.UpdateParticipants(ctx, command.SessionID, command.GroupJID, command.Participants, action)
	case application.GroupOpLeave:
		return domain.GroupInfo{}, a.live.Leave(ctx, command.SessionID, command.GroupJID)
	default:
		return domain.GroupInfo{}, domain.ErrValidation("invalid group operation")
	}
}

// replayedGroupMutation reconstructs a stored group-command outcome. Create
// replays carry the original group's live metadata; other kinds return routing
// metadata only. Failures replay as validation errors.
func replayedGroupMutation(command application.GroupMutationCommand, record *application.CommandResultRecord) (application.GroupCreateResult, error) {
	meta := mutationResult(record.CommandID, command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch)
	switch record.Status {
	case application.CommandSent:
		result := application.GroupCreateResult{MutationOnlyResult: application.MutationOnlyResult{MutationResult: meta}}
		if command.Kind == application.GroupOpCreate {
			result.CreatedGroup = application.GroupInfoResult{GroupJID: record.WAMessageID}
		}
		return result, nil
	case application.CommandFailed:
		return application.GroupCreateResult{}, domain.ErrValidation("group op previously failed: " + record.Error)
	default:
		return application.GroupCreateResult{}, fmt.Errorf("unknown stored command status %q", record.Status)
	}
}

// GetGroupInviteLink reads (reset=false) or revokes-and-regenerates (reset=true)
// a group's invite link. Not ledger-backed, mirroring the LiveOps surface.
func (a *ApplicationGatewayAdapter) GetGroupInviteLink(ctx context.Context, query application.SessionStateQuery, groupJID string, reset bool) (string, error) {
	if err := a.fenceQuery(query); err != nil {
		return "", err
	}
	if groupJID == "" {
		return "", domain.ErrValidation("group_jid is required")
	}
	return a.live.GetInviteLink(ctx, query.SessionID, groupJID, reset)
}

// JoinGroup joins a group from an invite code/link. Read-classified like its
// LiveOps counterpart: no ledger entry.
func (a *ApplicationGatewayAdapter) JoinGroup(ctx context.Context, query application.SessionStateQuery, invite string) (string, error) {
	if err := a.fenceQuery(query); err != nil {
		return "", err
	}
	if invite == "" {
		return "", domain.ErrValidation("invite is required")
	}
	return a.live.JoinWithLink(ctx, query.SessionID, invite)
}

// GetChatPresence subscribes to a contact's presence updates and returns the
// unknown snapshot. Read-only: no ledger.
func (a *ApplicationGatewayAdapter) GetChatPresence(ctx context.Context, query application.SessionStateQuery, chatJID string) (domain.PresenceStatus, error) {
	if err := a.fenceQuery(query); err != nil {
		return domain.PresenceStatus{}, err
	}
	if chatJID == "" {
		return domain.PresenceStatus{}, domain.ErrValidation("chat_jid is required")
	}
	return a.live.GetPresence(ctx, query.SessionID, chatJID)
}

// SetChatPresence sends per-chat typing state. Not ledger-backed: repeating a
// typing state is idempotent by construction and the legacy port never deduped.
func (a *ApplicationGatewayAdapter) SetChatPresence(ctx context.Context, command application.ChatPresenceCommand) error {
	query := sessionQueryFrom(command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch)
	if err := a.fenceQuery(query); err != nil {
		return err
	}
	switch command.State {
	case "composing", "paused", "recording":
	default:
		return domain.ErrValidation("invalid chat presence state")
	}
	return a.live.SetChatPresence(ctx, command.SessionID, command.ChatJID, command.State)
}

// BackfillSession pulls the session's direct-API snapshot. Slow read: no
// ledger, callers apply the send deadline.
func (a *ApplicationGatewayAdapter) BackfillSession(ctx context.Context, query application.SessionStateQuery) (domain.BackfillSnapshot, error) {
	if err := a.fenceQuery(query); err != nil {
		return domain.BackfillSnapshot{}, err
	}
	return a.live.BackfillSessionData(ctx, query.SessionID)
}

// ---- Session lifecycle slices (Increment 7) ----
//
// The API owns session rows, placement, and assignments; the gateway executes
// only the live parts. PrepareSession and ForgetSession validate the target
// alone: prepare is idempotent by construction and forget must still work when
// the row (and its assignment) is already deleted API-side. BeginPairing and
// PairPhone run behind the ownership fence but stay outside the ledger — a
// repeated QR read re-reads the live code, and each pairing code is a fresh
// one-time secret with nothing durable to replay. LogoutSession is destructive
// on WhatsApp's side, so it reuses the send pipeline's replay-then-fence-then-
// singleflight shape.

// sessionController is the manager surface the lifecycle slices drive.
type sessionController interface {
	EnsureDevice(id, organizationID string)
	StartQR(ctx context.Context, id string) error
	StartPairingCode(ctx context.Context, id, phone string) (string, error)
	Logout(ctx context.Context, id string) error
	Forget(id string)
	LatestQR(id string) (code string, expiresAt int64)
}

// PrepareSession materializes the keystore device + managed-session entry for
// an API-created session row. Idempotent; not ledger-backed.
func (a *ApplicationGatewayAdapter) PrepareSession(_ context.Context, query application.SessionStateQuery) (application.PrepareSessionResult, error) {
	if err := a.validateTarget(query.OrganizationID, query.SessionID, query.GatewayID); err != nil {
		return application.PrepareSessionResult{}, err
	}
	a.controller.EnsureDevice(query.SessionID, query.OrganizationID)
	return application.PrepareSessionResult{MutationResult: mutationResult("", query.OrganizationID, query.SessionID, query.GatewayID, query.AssignmentEpoch)}, nil
}

// BeginPairing starts (or resumes) QR pairing and returns the current snapshot
// code when one exists. Read-classified: no command_id, no ledger.
func (a *ApplicationGatewayAdapter) BeginPairing(ctx context.Context, query application.SessionStateQuery) (application.PairingSnapshot, error) {
	if err := a.fenceQuery(query); err != nil {
		return application.PairingSnapshot{}, err
	}
	if code, exp := a.controller.LatestQR(query.SessionID); code != "" {
		return application.PairingSnapshot{Code: code, ExpiresAt: exp}, nil
	}
	if err := a.controller.StartQR(ctx, query.SessionID); err != nil {
		return application.PairingSnapshot{}, err
	}
	code, exp := a.controller.LatestQR(query.SessionID)
	return application.PairingSnapshot{Code: code, ExpiresAt: exp}, nil
}

// PairPhone requests a phone-number pairing code. Not ledger-backed: every
// call yields a fresh one-time secret.
func (a *ApplicationGatewayAdapter) PairPhone(ctx context.Context, query application.SessionStateQuery, phone string) (string, error) {
	if err := a.fenceQuery(query); err != nil {
		return "", err
	}
	if phone == "" {
		return "", domain.ErrValidation("phone is required")
	}
	return a.controller.StartPairingCode(ctx, query.SessionID, phone)
}

// LogoutSession executes one durable logout command behind the assignment
// fence with full ledger semantics. Only CommandSent is recorded — logout has
// no WhatsApp message id — and stored failures replay as validation errors.
func (a *ApplicationGatewayAdapter) LogoutSession(ctx context.Context, command application.ContactJIDCommand) (application.MutationOnlyResult, error) {
	if err := a.validateMutation(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID); err != nil {
		return application.MutationOnlyResult{}, err
	}
	if command.AssignmentEpoch == 0 {
		return application.MutationOnlyResult{}, domain.ErrValidation("assignment_epoch must be at least 1")
	}
	record, err := a.lookupCommand(ctx, command.CommandID)
	if err != nil {
		return application.MutationOnlyResult{}, err
	}
	if record != nil {
		return replayedBlockingMutation(record)
	}
	if a.fence != nil && !a.fence.AllowsMutation(command.OrganizationID, command.SessionID, command.AssignmentEpoch) {
		return application.MutationOnlyResult{}, domain.ErrConflict("session assignment epoch is stale or lease expired")
	}
	flight, follower := a.joinInFlight(command.CommandID)
	if follower {
		select {
		case <-flight.done:
			return application.MutationOnlyResult{MutationResult: flight.result.MutationResult}, flight.err
		case <-ctx.Done():
			return application.MutationOnlyResult{}, ctx.Err()
		}
	}
	var (
		result application.MutationOnlyResult
		err2   error
	)
	defer func() {
		flight.result = application.SendMessageResult{MutationResult: result.MutationResult}
		flight.err = err2
		close(flight.done)
		a.leaveInFlight(command.CommandID)
	}()

	err2 = a.controller.Logout(ctx, command.SessionID)
	if err2 != nil {
		return application.MutationOnlyResult{}, err2
	}
	result = application.MutationOnlyResult{MutationResult: mutationResult(command.CommandID, command.OrganizationID, command.SessionID, command.GatewayID, command.AssignmentEpoch)}
	if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
		CommandID: command.CommandID, SessionID: command.SessionID,
		Status: application.CommandSent, UpdatedAt: a.now().UTC(),
	}); saveErr != nil {
		return application.MutationOnlyResult{}, saveErr
	}
	return result, nil
}

// ForgetSession drops a session's in-memory runtime during the delete flow.
// Idempotent; no epoch requirement because the row may already be deleted
// API-side — only the target is validated.
func (a *ApplicationGatewayAdapter) ForgetSession(_ context.Context, organizationID, sessionID string) error {
	if err := a.validateTarget(organizationID, sessionID, a.gatewayID); err != nil {
		return err
	}
	a.controller.Forget(sessionID)
	return nil
}

func sessionQueryFrom(organizationID, sessionID, gatewayID string, assignmentEpoch uint64) application.SessionStateQuery {
	return application.SessionStateQuery{OrganizationID: organizationID, SessionID: sessionID, GatewayID: gatewayID, AssignmentEpoch: assignmentEpoch}
}
