package wa

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
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

// ApplicationGatewayAdapter exposes existing in-process WhatsApp operations
// through the transport-independent application boundary. It is intentionally
// not wired into a request path yet.
type ApplicationGatewayAdapter struct {
	gatewayID     string
	sessions      sessionStateSource
	live          engineLiveOps
	now           func() time.Time
	maxFutureSkew time.Duration
	fence         assignmentFence
}

// DefaultReadReceiptFutureSkew tolerates small clock differences between the
// API and gateway without accepting timestamps far in the future.
const DefaultReadReceiptFutureSkew = 30 * time.Second

// NewApplicationGatewayAdapter constructs the local adapter used by the private
// gRPC server. The supplied fence is checked immediately before live operations.
func NewApplicationGatewayAdapter(gatewayID string, manager *Manager, fence assignmentFence) *ApplicationGatewayAdapter {
	return &ApplicationGatewayAdapter{
		gatewayID:     gatewayID,
		sessions:      manager,
		live:          manager.LiveOps(),
		now:           time.Now,
		maxFutureSkew: DefaultReadReceiptFutureSkew,
		fence:         fence,
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
