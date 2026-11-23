package service

import (
	"context"
	"log/slog"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

// SessionService owns the WhatsApp-session lifecycle (§3): create, list, get,
// the start/stop/restart/logout actions, delete, plus pairing surfaces (qr,
// pairing-code) and the /me identity. The API owns wa_sessions rows, placement,
// and assignments; every live part executes through private engine RPCs.
//
// User actions write only intent (desired state and start generation) through
// the session-state repo; the assigned gateway converges on it through its
// desired-state snapshot. After recording intent, an action also applies it on
// the gateway directly (ApplySession) so the user does not wait for the next
// heartbeat; that call is an accelerator, not the source of truth.
type SessionService struct {
	repo          *store.SessionRepo
	gateways      *store.GatewayRepo
	assignments   *store.GatewayAssignmentRepo
	liveFacade    GatewayLiveFacade
	log           *slog.Logger
	oauthCascader sessionOAuthCascader
	// state records intent (desired state, start generation, pairing clear).
	state sessionStateStore
	// gatewayFacade is the control-plane session-lifecycle boundary (Increment
	// 7). Apply/QR/pairing/logout/delete execute their live parts through
	// private engine RPCs while this service owns rows, placement, and
	// assignments.
	gatewayFacade GatewaySessionFacade
	// History sync: run reads plus the HISTORY_SYNC_* policy.
	history            *store.HistoryRepo
	historyDefaultDays int
	historyMaxDays     int
}

// SetGatewayAssignmentRepo supplies the assignment-revision repo used by the
// facade create/delete flows. It needs *sql.DB transactions, so it cannot ride
// the tx-bound store aggregate; the composition root injects it separately.
func (s *SessionService) SetGatewayAssignmentRepo(assignments *store.GatewayAssignmentRepo) {
	if assignments != nil {
		s.assignments = assignments
	}
}

// sessionStateStore records what users asked of a session.
type sessionStateStore interface {
	SetDesired(ctx context.Context, sessionID string, desired domain.DesiredState, newStart bool, at int64) (uint64, error)
	ClearPairing(ctx context.Context, sessionID string, at int64) error
}

// SetSessionStateStore supplies the intent writer. It needs *sql.DB
// transactions, so the composition root injects it.
func (s *SessionService) SetSessionStateStore(state sessionStateStore) {
	if state != nil {
		s.state = state
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
	// HistorySyncDays overrides the deployment default (nil = default).
	HistorySyncDays *int
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

// createControlled is the facade path of Create: the API picks a placement
// and atomically inserts the session, its runtime row and its assignment.
// With Start the session is created with desired state run, so the gateway
// begins QR pairing as soon as its next snapshot carries the assignment.
func (s *SessionService) createControlled(
	ctx context.Context,
	organizationID string,
	in CreateInput,
) (domain.WASession, error) {
	// Validate before placement so a bad value is a 400, not a placement error.
	historyDays, err := s.resolveHistorySyncDays(in.HistorySyncDays)
	if err != nil {
		return domain.WASession{}, err
	}
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
		HistorySyncDays: historyDays,
		ID:              domain.NewSessionID(),
		OrganizationID:  organizationID,
		GatewayID:       gatewayRow.ID,
		Label:           in.Label,
		Status:          domain.SessionStopped,
		StatusChangedAt: now,
		DesiredState:    domain.DesiredStop,
		AutoRead:        autoRead,
		PresenceTyping:  presence,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if in.Start {
		sess.DesiredState = domain.DesiredRun
		sess.StartGeneration = 1
	}
	if err := s.assignments.CreateSession(ctx, sess); err != nil {
		return domain.WASession{}, err
	}
	return s.Get(ctx, organizationID, sess.ID)
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

// Start asks for the session to run: a paired session connects, an unpaired
// or logged-out one begins QR pairing. A session that is already running is
// left alone; one whose last run ended (failed, logged out, stopped) gets a
// new start generation, which is what lets its gateway start it again.
func (s *SessionService) Start(ctx context.Context, organizationID, id string) error {
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return err
	}
	newStart := sess.DesiredState != domain.DesiredRun || !sess.Status.Live()
	_, err = s.requestRun(ctx, sess, newStart)
	return err
}

// Stop asks for the session to stop; pairing stays in place.
func (s *SessionService) Stop(ctx context.Context, organizationID, id string) error {
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return err
	}
	if s.state == nil {
		return errLiveUnavailable()
	}
	if _, err := s.state.SetDesired(ctx, sess.ID, domain.DesiredStop, false, domain.NowMs()); err != nil {
		return err
	}
	s.applyNow(ctx, sess, false, sess.StartGeneration)
	return nil
}

// Restart asks for a fresh run: a new start generation makes the gateway end
// the current run (if any) and start again, so an unpaired session begins a
// fresh QR pairing flow.
func (s *SessionService) Restart(ctx context.Context, organizationID, id string) error {
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return err
	}
	_, err = s.requestRun(ctx, sess, true)
	return err
}

// requestRun records desired state run (with a new start generation when
// newStart) and applies it on the gateway now. It returns the generation.
func (s *SessionService) requestRun(ctx context.Context, sess domain.WASession, newStart bool) (uint64, error) {
	if s.state == nil {
		return 0, errLiveUnavailable()
	}
	generation, err := s.state.SetDesired(ctx, sess.ID, domain.DesiredRun, newStart, domain.NowMs())
	if err != nil {
		return 0, err
	}
	s.applyNow(ctx, sess, true, generation)
	return generation, nil
}

// applyNow applies recorded intent on the gateway without waiting for its next
// desired-state snapshot. The intent is already durable and the snapshot
// carries it within one heartbeat, so a failure here (gateway down, assignment
// not yet delivered) is only logged.
func (s *SessionService) applyNow(ctx context.Context, sess domain.WASession, run bool, generation uint64) {
	if s.gatewayFacade == nil {
		return
	}
	device := ""
	if sess.WAJID != nil {
		device = *sess.WAJID
	}
	if err := s.gatewayFacade.Apply(ctx, sess.OrganizationID, sess.ID, run, generation, device); err != nil {
		s.log.InfoContext(ctx, "session intent recorded; the gateway applies it with its next snapshot",
			"session", sess.ID, "run", run, "start_generation", generation, "err", err)
	}
}

// Logout unlinks the device server-side, deletes its keystore device, and
// resets pairing: the engine unlink runs as a durable command and must be
// confirmed by WhatsApp, then the row atomically loses its WhatsApp identity
// and its desired state becomes stop, so the session stays logged out until
// the user starts it again. The OAuth cascade order (logout first, then
// cascade) matches the legacy path.
func (s *SessionService) Logout(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	if s.gatewayFacade == nil || s.state == nil {
		return errLiveUnavailable()
	}
	if err := s.gatewayFacade.Logout(ctx, organizationID, id); err != nil {
		return err
	}
	// The gateway's logged_out status also clears the identity when it is
	// projected; this synchronous write keeps the REST contract immediate.
	if err := s.state.ClearPairing(ctx, id, domain.NowMs()); err != nil {
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
	return s.assignments.DeleteSession(ctx, id, domain.NowMs())
}

func (s *SessionService) cascadeOAuth(ctx context.Context, organizationID, id string) error {
	if s.oauthCascader == nil {
		return nil
	}
	return s.oauthCascader.CascadeSessionLogoutOrDelete(ctx, organizationID, id)
}

// Me describes the attached WhatsApp identity for GET /sessions/{id}/me.
type Me struct {
	SessionID string               `json:"sessionId"`
	Status    domain.SessionStatus `json:"status" enum:"starting,scan_qr_code,working,reconnecting,failed,stopped,logged_out" doc:"The session's live status on its gateway."`
	// FailureReason is set when status is failed.
	FailureReason *domain.SessionFailureReason `json:"failureReason,omitempty" enum:"passkey_failed,qr_timeout,pairing_error,client_outdated,stream_replaced,temporary_ban,connect_rejected,connect_error" doc:"Why the session failed; present only when status is failed."`
	WAJID         *string                      `json:"waJid,omitempty"`
	WALID         *string                      `json:"waLid,omitempty"`
	PhoneNumber   *string                      `json:"phoneNumber,omitempty"`
	Connected     bool                         `json:"connected"`
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
	failure := sess.FailureReason
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
		failure = nil
		if state.Status == domain.SessionFailed && state.FailureReason != "" {
			reason := state.FailureReason
			failure = &reason
		}
	}
	return Me{
		SessionID:     sess.ID,
		Status:        status,
		FailureReason: failure,
		WAJID:         sess.WAJID,
		WALID:         sess.WALID,
		PhoneNumber:   sess.PhoneNumber,
		Connected:     connected,
	}, nil
}

// QR is the response for GET /sessions/{id}/qr.
type QR struct {
	State     string `json:"state" enum:"pending,ready" doc:"ready: code holds the current QR code. pending: the session is pairing but WhatsApp has not sent a code yet (the gateway may still be connecting); poll again or wait for the auth.qr event." example:"ready"`
	Code      string `json:"code,omitempty" doc:"The QR code content to render as a QR image. Present only when state is ready." example:"2@abc123..."`
	ExpiresAt int64  `json:"expiresAt,omitempty" doc:"When the code stops being valid, in epoch milliseconds (UTC). Present only when state is ready and the expiry is known." example:"1719662460000"`
}

// QR pairing states.
const (
	QRPending = "pending"
	QRReady   = "ready"
)

// QR returns the session's current pairing QR code. It only reads: starting
// pairing is the start action's job. A session that is pairing answers ready
// with its code or pending while no code exists yet; a session whose pairing
// failed answers 409 with the failure reason; a session that is not pairing
// answers 409 telling the caller to start it. Codes also stream live over the
// events channel (auth.qr); this is the snapshot for a one-shot poll.
func (s *SessionService) QR(ctx context.Context, organizationID, id string) (QR, error) {
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return QR{}, err
	}
	if sess.WAJID != nil {
		return QR{}, domain.ErrConflict("session is already paired")
	}
	if err := qrPrecondition(sess); err != nil {
		return QR{}, err
	}
	if s.gatewayFacade == nil {
		return QR{}, errLiveUnavailable()
	}
	snapshot, err := s.gatewayFacade.QR(ctx, organizationID, sess.ID)
	if err != nil {
		return QR{}, err
	}
	if snapshot.Code == "" {
		return QR{State: QRPending}, nil
	}
	return QR{State: QRReady, Code: snapshot.Code, ExpiresAt: snapshot.ExpiresAt}, nil
}

// qrPrecondition rejects a QR read for an unpaired session that is not
// pairing, from its recorded intent and observed status alone.
func qrPrecondition(sess domain.WASession) error {
	switch {
	case sess.Status == domain.SessionUnavailable:
		return domain.ErrUnavailable("the gateway that holds this session is not available right now; retry later")
	case sess.Status == domain.SessionFailed && sess.DesiredState == domain.DesiredRun:
		details := map[string]any{"status": string(sess.Status)}
		message := "pairing failed; start the session again for a new QR code"
		if sess.FailureReason != nil {
			details["failureReason"] = string(*sess.FailureReason)
			message = "pairing failed (" + string(*sess.FailureReason) + "); start the session again for a new QR code"
		}
		return domain.ErrConflict(message).WithDetails(details)
	case sess.Status == domain.SessionLoggedOut && sess.DesiredState == domain.DesiredRun:
		return domain.ErrConflict("session was logged out; start it again for a new QR code").
			WithDetails(map[string]any{"status": string(sess.Status)})
	case sess.DesiredState != domain.DesiredRun:
		return domain.ErrConflict("session is not pairing; start it to get a QR code").
			WithDetails(map[string]any{"status": string(sess.Status)})
	}
	return nil
}

// SubmitPasskey forwards the user's WebAuthn assertion for the passkey
// verification WhatsApp requested while linking the session.
func (s *SessionService) SubmitPasskey(ctx context.Context, organizationID, id string, response []byte) error {
	if len(response) == 0 {
		return domain.ErrValidation("response is required")
	}
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return err
	}
	if sess.WAJID != nil {
		return domain.ErrConflict("session is already paired")
	}
	if s.gatewayFacade == nil {
		return errLiveUnavailable()
	}
	return s.gatewayFacade.SubmitPasskey(ctx, organizationID, sess.ID, response)
}

// ConfirmPasskey confirms the user saw the same passkey pairing code on the phone.
func (s *SessionService) ConfirmPasskey(ctx context.Context, organizationID, id string) error {
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return err
	}
	if sess.WAJID != nil {
		return domain.ErrConflict("session is already paired")
	}
	if s.gatewayFacade == nil {
		return errLiveUnavailable()
	}
	return s.gatewayFacade.ConfirmPasskey(ctx, organizationID, sess.ID)
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
	if s.gatewayFacade == nil || s.state == nil {
		return "", errLiveUnavailable()
	}
	// Asking for a code is asking for the session to pair: record it like a
	// start, then apply it on the gateway now, since the code is requested on
	// the running pairing connection.
	newStart := sess.DesiredState != domain.DesiredRun || !sess.Status.Live()
	generation, err := s.state.SetDesired(ctx, sess.ID, domain.DesiredRun, newStart, domain.NowMs())
	if err != nil {
		return "", err
	}
	if err := s.gatewayFacade.Apply(ctx, organizationID, sess.ID, true, generation, ""); err != nil {
		return "", err
	}
	return s.gatewayFacade.PairingCode(ctx, organizationID, sess.ID, phone)
}
