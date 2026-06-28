package handlers

import (
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// AdminListSessions handles GET /admin/sessions (super_admin cross-organization view).
func (h *Handlers) AdminListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.Admin.ListAllSessions(r.Context())
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.ListEnvelope(w, sessions, "")
}

// AdminStartSessionBackfill handles POST /admin/sessions/{session}:backfill.
func (h *Handlers) AdminStartSessionBackfill(w http.ResponseWriter, r *http.Request) {
	job, err := h.Admin.StartBackfill(r.Context(), param(r, "session"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, job)
}

// AdminSessionBackfillStatus handles GET /admin/sessions/{session}/backfill.
func (h *Handlers) AdminSessionBackfillStatus(w http.ResponseWriter, r *http.Request) {
	job, err := h.Admin.BackfillStatus(r.Context(), param(r, "session"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, job)
}
