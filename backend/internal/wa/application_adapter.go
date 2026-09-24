package wa

import (
	"context"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
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
	OwnsAssignment(string, string, uint64) bool
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
	inFlight   map[string]*commandFlight
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
		inFlight:      map[string]*commandFlight{},
	}
}

var _ application.GatewayEngine = (*ApplicationGatewayAdapter)(nil)

func (a *ApplicationGatewayAdapter) GetSessionState(
	_ context.Context,
	query application.SessionStateQuery,
) (application.SessionState, error) {
	if err := a.validateTarget(
		query.OrganizationID,
		query.SessionID,
		query.GatewayID,
	); err != nil {
		return application.SessionState{}, err
	}
	if a.fence != nil {
		ownsAssignment := query.AssignmentEpoch != 0 &&
			a.fence.OwnsSession(query.OrganizationID, query.SessionID, query.AssignmentEpoch)
		if !ownsAssignment {
			return application.SessionState{}, domain.ErrConflict("session assignment is not live")
		}
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

func (a *ApplicationGatewayAdapter) SetAccountPresence(
	ctx context.Context,
	command application.SetPresenceCommand,
) (application.MutationResult, error) {
	if err := a.validateMutation(
		command.CommandID,
		command.OrganizationID,
		command.SessionID,
		command.GatewayID,
	); err != nil {
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
	return mutationResult(
		command.CommandID,
		command.OrganizationID,
		command.SessionID,
		command.GatewayID,
		command.AssignmentEpoch,
	), nil
}

func (a *ApplicationGatewayAdapter) MarkRead(
	ctx context.Context,
	command application.MarkReadCommand,
) (application.MutationResult, error) {
	if err := a.validateMutation(
		command.CommandID,
		command.OrganizationID,
		command.SessionID,
		command.GatewayID,
	); err != nil {
		return application.MutationResult{}, err
	}
	if command.AssignmentEpoch == 0 {
		return application.MutationResult{}, domain.ErrValidation("assignment_epoch must be at least 1")
	}
	if a.fence != nil && !a.fence.AllowsMutation(command.OrganizationID, command.SessionID, command.AssignmentEpoch) {
		return application.MutationResult{}, domain.ErrConflict("session assignment epoch is stale or lease expired")
	}
	missingReceipt := command.ChatJID == "" || len(command.MessageIDs) == 0 || command.ReadAt.IsZero()
	if missingReceipt {
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
	if err := a.live.SendReadReceiptAt(
		ctx,
		command.SessionID,
		command.ChatJID,
		command.SenderJID,
		command.MessageIDs,
		command.ReadAt,
	); err != nil {
		return application.MutationResult{}, err
	}
	return mutationResult(
		command.CommandID,
		command.OrganizationID,
		command.SessionID,
		command.GatewayID,
		command.AssignmentEpoch,
	), nil
}
