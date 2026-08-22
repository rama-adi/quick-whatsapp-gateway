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
	manager       *wa.Manager
	liveFacade    GatewayLiveFacade
	log           *slog.Logger
	oauthCascader sessionOAuthCascader
	// desiredController is the API-local lifecycle boundary (Increment 7):
	// start/stop/restart flip the session's desired run state and advance the
	// assignment revision; the assigned gateway reconciles. When nil, Start/
	// Stop/Restart fall back to direct manager calls (legacy path).
	desiredController SessionDesiredController
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

type sessionOAuthCascader interface {
	CascadeSessionLogoutOrDelete(ctx context.Context, org, sessionID string) error
}

// NewSessionService constructs a SessionService.
func NewSessionService(repo *store.SessionRepo, manager *wa.Manager, log *slog.Logger) *SessionService {
	if log == nil {
		log = slog.Default()
	}
	return &SessionService{repo: repo, manager: manager, log: log}
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

// Create provisions a new session for the organization. The manager mints the row
// (id + defaults), then we optionally start QR pairing when Start is requested.
func (s *SessionService) Create(ctx context.Context, organizationID string, in CreateInput) (domain.WASession, error) {
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
// the session logged out.
func (s *SessionService) Logout(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	if s.manager == nil {
		return errLiveUnavailable()
	}
	if err := s.manager.Logout(ctx, id); err != nil {
		return err
	}
	return s.cascadeOAuth(ctx, organizationID, id)
}

// Delete tears down the live session and removes its persisted row.
func (s *SessionService) Delete(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	if s.manager == nil {
		return errLiveUnavailable()
	}
	if err := s.cascadeOAuth(ctx, organizationID, id); err != nil {
		return err
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
	if s.manager == nil {
		return "", errLiveUnavailable()
	}
	return s.manager.StartPairingCode(ctx, id, phone)
}
