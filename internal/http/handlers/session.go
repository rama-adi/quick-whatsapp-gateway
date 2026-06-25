package handlers

import (
	"context"
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
)

// createSessionBody is the POST /sessions request.
type createSessionBody struct {
	Label          *string `json:"label,omitempty"`
	Start          bool    `json:"start,omitempty"`
	AutoRead       *bool   `json:"autoRead,omitempty"`
	PresenceTyping *bool   `json:"presenceTyping,omitempty"`
}

// CreateSession handles POST /sessions.
func (h *Handlers) CreateSession(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	var body createSessionBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	sess, err := h.Sessions.Create(r.Context(), tenantID, service.CreateInput{
		Label:          body.Label,
		Start:          body.Start,
		AutoRead:       body.AutoRead,
		PresenceTyping: body.PresenceTyping,
	})
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, sess)
}

// ListSessions handles GET /sessions.
func (h *Handlers) ListSessions(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	sessions, err := h.Sessions.List(r.Context(), tenantID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.ListEnvelope(w, sessions, "")
}

// GetSession handles GET /sessions/{id}.
func (h *Handlers) GetSession(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	sess, err := h.Sessions.Get(r.Context(), tenantID, param(r, "session"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sess)
}

// StartSession handles POST /sessions/{id}:start.
func (h *Handlers) StartSession(w http.ResponseWriter, r *http.Request) {
	h.sessionAction(w, r, h.Sessions.Start)
}

// StopSession handles POST /sessions/{id}:stop.
func (h *Handlers) StopSession(w http.ResponseWriter, r *http.Request) {
	h.sessionAction(w, r, h.Sessions.Stop)
}

// RestartSession handles POST /sessions/{id}:restart.
func (h *Handlers) RestartSession(w http.ResponseWriter, r *http.Request) {
	h.sessionAction(w, r, h.Sessions.Restart)
}

// LogoutSession handles POST /sessions/{id}:logout.
func (h *Handlers) LogoutSession(w http.ResponseWriter, r *http.Request) {
	h.sessionAction(w, r, h.Sessions.Logout)
}

// sessionAction is the shared body for the no-payload lifecycle actions
// (:start, :stop, :restart, :logout). On success it returns the refreshed
// session row so the client sees the new status.
func (h *Handlers) sessionAction(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, tenantID, id string) error) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	id := param(r, "session")
	if err := fn(r.Context(), tenantID, id); err != nil {
		httpx.WriteError(w, err)
		return
	}
	sess, err := h.Sessions.Get(r.Context(), tenantID, id)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sess)
}

// DeleteSession handles DELETE /sessions/{id}.
func (h *Handlers) DeleteSession(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	if err := h.Sessions.Delete(r.Context(), tenantID, param(r, "session")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SessionMe handles GET /sessions/{id}/me.
func (h *Handlers) SessionMe(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	me, err := h.Sessions.Me(r.Context(), tenantID, param(r, "session"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, me)
}

// SessionQR handles GET /sessions/{id}/qr.
func (h *Handlers) SessionQR(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	qr, err := h.Sessions.QR(r.Context(), tenantID, param(r, "session"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, qr)
}

// pairingCodeBody is the POST /sessions/{id}/pairing-code request.
type pairingCodeBody struct {
	Phone string `json:"phone"`
}

// SessionPairingCode handles POST /sessions/{id}/pairing-code.
func (h *Handlers) SessionPairingCode(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	var body pairingCodeBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	code, err := h.Sessions.PairingCode(r.Context(), tenantID, param(r, "session"), body.Phone)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"code": code})
}
