package service

import (
	"context"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// SessionService owns the WhatsApp-session lifecycle (§3): create, list, get,
// the start/stop/restart/logout actions, delete, plus pairing surfaces (qr,
// pairing-code) and the /me identity. The API owns wa_sessions rows, placement,
// and assignments; every live part executes through private engine RPCs.
type SessionService struct {
	repo          *store.SessionRepo
	gateways      *store.GatewayRepo
	assignments   *store.GatewayAssignmentRepo
	liveFacade    GatewayLiveFacade
	log           *slog.Logger
	oauthCascader sessionOAuthCascader
	// desiredController is the API-local lifecycle boundary (Increment 7):
	// start/stop/restart flip the session's desired run state and advance the
	// assignment revision; the assigned gateway reconciles.
	desiredController SessionDesiredController
	// gatewayFacade is the control-plane session-lifecycle boundary (Increment
	// 7). Create/QR/pairing/logout/delete execute their live parts through
	// private engine RPCs while this service owns rows, placement, and
	// assignments.
	gatewayFacade GatewaySessionFacade
}

// SetGatewayAssignmentRepo supplies the assignment-revision repo used by the
// facade create/delete flows. It needs *sql.DB transactions, so it cannot ride
// the tx-bound store aggregate; the composition root injects it separately.
func (s *SessionService) SetGatewayAssignmentRepo(assignments *store.GatewayAssignmentRepo) {
	if assignments != nil {
		s.assignments = assignments
	}
}

// SessionDesiredController flips a session's authoritative run state without
// touching a gateway directly.
type SessionDesiredController interface {
	SetSessionDesired(ctx context.Context, sessionID string, run bool) error
}

// SetSessionDesiredController routes Start/Stop/Restart through desired-state
// reconciliation instead of direct gateway calls.
func (s *SessionService) SetSessionDesiredController(controller SessionDesiredController) {
	if controller != nil {
		s.desiredController = controller
	}
}

// SetGatewaySessionFacade routes the session lifecycle's live parts through
// the API-owned, resolved engine facade. Gateway-local composition keeps its
// in-process manager until API composition supplies this seam.
func (s *SessionService) SetGatewaySessionFacade(facade GatewaySessionFacade) {
	if facade != nil {
		s.gatewayFacade = facade
	}
}

type sessionOAuthCascader interface {
	CascadeSessionLogoutOrDelete(ctx context.Context, org, sessionID string) error
}

// NewSessionService constructs a SessionService. gateways and the assignment
// repo are required for the facade (control-plane) create flow.
func NewSessionService(repo *store.SessionRepo, gateways *store.GatewayRepo, log *slog.Logger) *SessionService {
	if log == nil {
		log = slog.Default()
	}
	return &SessionService{repo: repo, gateways: gateways, log: log}
}

func (s *SessionService) SetOAuthCascader(c sessionOAuthCascader) {
	s.oauthCascader = c
}

// SetGatewayLiveFacade switches live session-state reads to the API-owned,
// resolved gateway facade. Gateway-local composition deliberately leaves this
// unset until API composition wires the resolver/facade.
func (s *SessionService) SetGatewayLiveFacade(facade GatewayLiveFacade) {
	s.liveFacade = facade
}

// CreateInput is the body of POST /sessions.
type CreateInput struct {
	Label          *string
	Start          bool
	AutoRead       *bool
	PresenceTyping *bool
}

// Create provisions a new session for the organization: the API picks the
// placement, owns the row + assignment, and the engine prepares its pairing
// substrate. QR pairing optionally kicks off when Start is requested.
func (s *SessionService) Create(ctx context.Context, organizationID string, in CreateInput) (domain.WASession, error) {
	if s.gatewayFacade == nil {
		return domain.WASession{}, errLiveUnavailable()
	}
	return s.createControlled(ctx, organizationID, in)
}

// createControlled is the facade path of Create: the API picks a placement,
// inserts the session row, records the assignment, then has the assigned
// engine materialize its keystore device. A QR-kick failure keeps the row and
// assignment (the session is retryable via POST .../qr) but is surfaced.
func (s *SessionService) createControlled(
	ctx context.Context,
	organizationID string,
	in CreateInput,
) (domain.WASession, error) {
	gatewayRow, err := s.gateways.PickForPlacement(ctx)
	if err != nil {
		return domain.WASession{}, domain.ErrUnavailable("no eligible gateway available for placement")
	}
	autoRead := true
	if in.AutoRead != nil {
		autoRead = *in.AutoRead
	}
	presence := false
	if in.PresenceTyping != nil {
		presence = *in.PresenceTyping
	}
	now := domain.NowMs()
	sess := domain.WASession{
		ID:             domain.NewSessionID(),
		OrganizationID: organizationID,
		GatewayID:      gatewayRow.ID,
		Label:          in.Label,
		Status:         domain.SessionStopped,
		AutoRead:       autoRead,
		PresenceTyping: presence,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.repo.Create(ctx, sess); err != nil {
		return domain.WASession{}, err
	}
	if _, err := s.assignments.Assign(ctx, sess.ID, gatewayRow.ID, now); err != nil {
		return domain.WASession{}, err
	}
	if err := s.gatewayFacade.Prepare(ctx, organizationID, sess.ID); err != nil {
		return sess, err
	}
	if !in.Start {
		return sess, nil
	}
	if _, err := s.gatewayFacade.QR(ctx, organizationID, sess.ID); err != nil {
		// Pairing kickoff failed, but the row exists — surface the error.
		return sess, err
	}
	// Pairing kicked off; the first code arrives asynchronously over events.
	return sess, nil
}

// List returns all sessions owned by the organization.
func (s *SessionService) List(ctx context.Context, organizationID string) ([]domain.WASession, error) {
	return s.repo.ListByOrg(ctx, organizationID)
}

// Get loads a single session, enforcing organization ownership.
func (s *SessionService) Get(ctx context.Context, organizationID, id string) (domain.WASession, error) {
	sess, err := s.repo.Get(ctx, id)
	if err != nil {
		return domain.WASession{}, err
	}
	if sess.OrganizationID != organizationID {
		return domain.WASession{}, domain.ErrNotFound("session not found")
	}
	return sess, nil
}

// Start connects a paired session or begins QR pairing for an unpaired one.
// Desired run-state alone can't express the unpaired case — the reconciler
// skips assignments without a device JID — so an unpaired or logged-out session
// also kicks QR pairing on its assigned engine before flipping the run state.
func (s *SessionService) Start(ctx context.Context, organizationID, id string) error {
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return err
	}
	return s.startSession(ctx, organizationID, sess)
}

func (s *SessionService) startSession(
	ctx context.Context,
	organizationID string,
	sess domain.WASession,
) error {
	if s.desiredController == nil {
		return errLiveUnavailable()
	}
	if sess.WAJID == nil {
		if s.gatewayFacade == nil {
			return errLiveUnavailable()
		}
		if err := s.gatewayFacade.Prepare(ctx, organizationID, sess.ID); err != nil {
			return err
		}
		if _, err := s.gatewayFacade.QR(ctx, organizationID, sess.ID); err != nil {
			return err
		}
	}
	return s.desiredController.SetSessionDesired(ctx, sess.ID, true)
}

// Stop disconnects a session and marks it stopped.
func (s *SessionService) Stop(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	if s.desiredController == nil {
		return errLiveUnavailable()
	}
	return s.desiredController.SetSessionDesired(ctx, id, false)
}

// Restart stops then starts a session. Start's unpaired transition applies, so
// restart also begins a fresh QR pairing flow rather than partially stopping and
// then failing validation.
func (s *SessionService) Restart(ctx context.Context, organizationID, id string) error {
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return err
	}
	if s.desiredController == nil || s.gatewayFacade == nil {
		return errLiveUnavailable()
	}
	// Forget synchronously disconnects the runtime while preserving its SQLite
	// device. A stop→start desired-state pair can coalesce before reconciliation
	// and therefore does not reliably restart the live runtime.
	if err := s.gatewayFacade.Forget(ctx, organizationID, id); err != nil {
		return err
	}
	return s.startSession(ctx, organizationID, sess)
}

// Logout unlinks the device server-side, deletes its keystore device, and
// resets pairing: the engine unlink runs as a durable command, then the row is
// atomically marked logged_out with its WhatsApp identity cleared so
// re-pairing can start immediately. The clear runs even when a previous logout
// already wrote logged_out, repairing rows left stale by older versions. The
// OAuth cascade order (logout first, then cascade) matches the legacy path.
func (s *SessionService) Logout(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	if s.gatewayFacade == nil {
		return errLiveUnavailable()
	}
	if err := s.gatewayFacade.Logout(ctx, organizationID, id); err != nil {
		return err
	}
	// The gateway's terminal session.status event also projects this clear; the
	// synchronous write here keeps the REST contract immediate rather than
	// worker-lagged.
	if err := s.repo.ClearPairing(ctx, id, domain.NowMs()); err != nil {
		return err
	}
	return s.cascadeOAuth(ctx, organizationID, id)
}

// Delete tears down the live session and removes its persisted row: cascade
// OAuth, have the engine drop its in-memory runtime, delete the row, then
// unassign so the gateway stops reconciling the session.
func (s *SessionService) Delete(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	if s.gatewayFacade == nil {
		return errLiveUnavailable()
	}
	if err := s.cascadeOAuth(ctx, organizationID, id); err != nil {
		return err
	}
	if err := s.gatewayFacade.Forget(ctx, organizationID, id); err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	return s.assignments.Unassign(ctx, id, domain.NowMs())
}

func (s *SessionService) cascadeOAuth(ctx context.Context, organizationID, id string) error {
	if s.oauthCascader == nil {
		return nil
	}
	return s.oauthCascader.CascadeSessionLogoutOrDelete(ctx, organizationID, id)
}

// Me describes the attached WhatsApp identity for GET /sessions/{id}/me.
type Me struct {
	SessionID   string               `json:"sessionId"`
	Status      domain.SessionStatus `json:"status"`
	WAJID       *string              `json:"waJid,omitempty"`
	WALID       *string              `json:"waLid,omitempty"`
	PhoneNumber *string              `json:"phoneNumber,omitempty"`
	Connected   bool                 `json:"connected"`
}

// Me returns the attached identity for a session.
func (s *SessionService) Me(ctx context.Context, organizationID, id string) (Me, error) {
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return Me{}, err
	}
	if sess.WAJID == nil {
		return Me{}, domain.ErrNotFound("session is not paired")
	}
	status := sess.Status
	connected := status == domain.SessionWorking
	if s.liveFacade != nil {
		state, err := s.liveFacade.GetSessionState(ctx, organizationID, sess.ID)
		if err != nil {
			return Me{}, err
		}
		stateOwned := state.OrganizationID == organizationID &&
			state.SessionID == sess.ID &&
			state.GatewayID == sess.GatewayID
		if !stateOwned {
			return Me{}, domain.ErrConflict("gateway returned mismatched session state")
		}
		status, connected = state.Status, state.Connected
	}
	return Me{
		SessionID:   sess.ID,
		Status:      status,
		WAJID:       sess.WAJID,
		WALID:       sess.WALID,
		PhoneNumber: sess.PhoneNumber,
		Connected:   connected,
	}, nil
}

// QR is the response for GET /sessions/{id}/qr.
type QR struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
}

// QR returns the current pairing QR code for a session, starting QR pairing if
// the session is not already streaming codes. Codes also stream live over the
// events channel (auth.qr); this is the snapshot for a one-shot poll.
func (s *SessionService) QR(ctx context.Context, organizationID, id string) (QR, error) {
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return QR{}, err
	}
	if sess.WAJID != nil {
		return QR{}, domain.ErrConflict("session is already paired")
	}
	if s.gatewayFacade == nil {
		return QR{}, errLiveUnavailable()
	}
	snapshot, err := s.gatewayFacade.QR(ctx, organizationID, sess.ID)
	if err != nil {
		return QR{}, err
	}
	if snapshot.Code == "" {
		// Pairing is starting; the first code arrives asynchronously over events.
		return QR{}, domain.ErrNotFound("qr code not ready yet; subscribe to events (auth.qr)")
	}
	return QR{Code: snapshot.Code, ExpiresAt: snapshot.ExpiresAt}, nil
}

// PairingCode requests a phone-number pairing code for a session.
func (s *SessionService) PairingCode(ctx context.Context, organizationID, id, phone string) (string, error) {
	if phone == "" {
		return "", domain.ErrValidation("phone is required")
	}
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return "", err
	}
	if sess.WAJID != nil {
		return "", domain.ErrConflict("session is already paired")
	}
	if s.gatewayFacade == nil {
		return "", errLiveUnavailable()
	}
	return s.gatewayFacade.PairingCode(ctx, organizationID, sess.ID, phone)
}
