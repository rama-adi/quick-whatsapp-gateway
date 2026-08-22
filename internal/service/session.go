package service

import (
	"context"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa"
)

// SessionService owns the WhatsApp-session lifecycle (§3): create, list, get,
// the start/stop/restart/logout actions, delete, plus pairing surfaces (qr,
// pairing-code) and the /me identity. It coordinates the in-memory wa.Manager
// (live clients) with the persisted wa_sessions rows (store.SessionRepo).
type SessionService struct {
	repo          *store.SessionRepo
	gateways      *store.GatewayRepo
	assignments   *store.GatewayAssignmentRepo
	manager       *wa.Manager
	liveFacade    GatewayLiveFacade
	log           *slog.Logger
	oauthCascader sessionOAuthCascader
	// desiredController is the API-local lifecycle boundary (Increment 7):
	// start/stop/restart flip the session's desired run state and advance the
	// assignment revision; the assigned gateway reconciles. When nil, Start/
	// Stop/Restart fall back to direct manager calls (legacy path).
	desiredController SessionDesiredController
	// gatewayFacade is the control-plane session-lifecycle boundary (Increment
	// 7). When set, create/QR/pairing/logout/delete execute their live parts
	// through private engine RPCs and this service owns rows, placement, and
	// assignments; the legacy in-process manager remains the fallback.
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

// NewSessionService constructs a SessionService. gateways is required only for
// the facade (control-plane) create flow; the legacy manager path ignores it,
// so gateway-local composition may pass nil.
func NewSessionService(repo *store.SessionRepo, gateways *store.GatewayRepo, manager *wa.Manager, log *slog.Logger) *SessionService {
	if log == nil {
		log = slog.Default()
	}
	return &SessionService{repo: repo, gateways: gateways, manager: manager, log: log}
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

// Create provisions a new session for the organization. When the gateway
// facade is set, the API owns the row (placement pick + repo insert +
// assignment) and the engine only prepares its pairing substrate; otherwise
// the in-process manager mints the row (legacy path). Both paths optionally
// kick off QR pairing when Start is requested.
func (s *SessionService) Create(ctx context.Context, organizationID string, in CreateInput) (domain.WASession, error) {
	if s.gatewayFacade != nil {
		return s.createControlled(ctx, organizationID, in)
	}
	if s.manager == nil {
		return domain.WASession{}, errLiveUnavailable()
	}
	autoRead := true
	if in.AutoRead != nil {
		autoRead = *in.AutoRead
	}
	presence := false
	if in.PresenceTyping != nil {
		presence = *in.PresenceTyping
	}
	sess, err := s.manager.CreateSession(ctx, organizationID, in.Label, autoRead, presence)
	if err != nil {
		return domain.WASession{}, err
	}
	if in.Start {
		if err := s.manager.StartQR(ctx, sess.ID); err != nil {
			// Pairing kickoff failed, but the row exists — surface the error.
			return *sess, err
		}
	}
	return *sess, nil
}

// createControlled is the facade path of Create: the API picks a placement,
// inserts the session row, records the assignment, then has the assigned
// engine materialize its keystore device. A QR-kick failure keeps the row and
// assignment (the session is retryable via POST .../qr) but is surfaced.
func (s *SessionService) createControlled(ctx context.Context, organizationID string, in CreateInput) (domain.WASession, error) {
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

// Start connects an already-paired session.
func (s *SessionService) Start(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	if s.desiredController != nil {
		return s.desiredController.SetSessionDesired(ctx, id, true)
	}
	return s.manager.Start(ctx, id)
}

// Stop disconnects a session and marks it stopped.
func (s *SessionService) Stop(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	if s.desiredController != nil {
		return s.desiredController.SetSessionDesired(ctx, id, false)
	}
	return s.manager.Stop(ctx, id)
}

// Restart stops then starts a session.
func (s *SessionService) Restart(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	if s.desiredController != nil {
		// One stop→start cycle through desired state; the reconciler converges
		// on the final run state.
		if err := s.desiredController.SetSessionDesired(ctx, id, false); err != nil {
			return err
		}
		return s.desiredController.SetSessionDesired(ctx, id, true)
	}
	return s.manager.Restart(ctx, id)
}

// Logout unlinks the device server-side, deletes its keystore device, and marks
// the session logged out. With the facade set the unlink runs as a durable
// engine command; the OAuth cascade order (logout first, then cascade) matches
// the legacy path.
func (s *SessionService) Logout(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	if s.gatewayFacade != nil {
		if err := s.gatewayFacade.Logout(ctx, organizationID, id); err != nil {
			return err
		}
		return s.cascadeOAuth(ctx, organizationID, id)
	}
	if s.manager == nil {
		return errLiveUnavailable()
	}
	if err := s.manager.Logout(ctx, id); err != nil {
		return err
	}
	return s.cascadeOAuth(ctx, organizationID, id)
}

// Delete tears down the live session and removes its persisted row. Facade
// path: cascade OAuth, have the engine drop its in-memory runtime, delete the
// row, then unassign so the gateway stops reconciling the session.
func (s *SessionService) Delete(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	if s.gatewayFacade == nil && s.manager == nil {
		return errLiveUnavailable()
	}
	if err := s.cascadeOAuth(ctx, organizationID, id); err != nil {
		return err
	}
	if s.gatewayFacade != nil {
		if err := s.gatewayFacade.Forget(ctx, organizationID, id); err != nil {
			return err
		}
		if err := s.repo.Delete(ctx, id); err != nil {
			return err
		}
		return s.assignments.Unassign(ctx, id, domain.NowMs())
	}
	s.manager.Forget(id)
	return s.repo.Delete(ctx, id)
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
		if state.OrganizationID != organizationID || state.SessionID != sess.ID || state.GatewayID != sess.GatewayID {
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
	if s.gatewayFacade != nil {
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
	if s.manager == nil {
		return QR{}, errLiveUnavailable()
	}
	ms := s.manager.Get(id)
	if ms == nil {
		return QR{}, domain.ErrNotFound("session not found")
	}
	if code, exp := ms.LatestQR(); code != "" {
		return QR{Code: code, ExpiresAt: exp}, nil
	}
	// No code yet: kick off QR pairing so the events stream (and a subsequent
	// poll) receives one.
	if err := s.manager.StartQR(ctx, id); err != nil {
		return QR{}, err
	}
	if code, exp := ms.LatestQR(); code != "" {
		return QR{Code: code, ExpiresAt: exp}, nil
	}
	// Pairing is starting; the first code arrives asynchronously over events.
	return QR{}, domain.ErrNotFound("qr code not ready yet; subscribe to events (auth.qr)")
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
	if s.gatewayFacade != nil {
		return s.gatewayFacade.PairingCode(ctx, organizationID, sess.ID, phone)
	}
	if s.manager == nil {
		return "", errLiveUnavailable()
	}
	return s.manager.StartPairingCode(ctx, id, phone)
}
