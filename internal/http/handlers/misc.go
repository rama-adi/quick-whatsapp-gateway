package handlers

import (
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// Events handles GET /events as a live NDJSON stream by delegating to the
// internal/stream handler (§9). Auth + organization enrichment happen in middleware;
// the stream handler reads the organization from context.
func (h *Handlers) Events(w http.ResponseWriter, r *http.Request) {
	if h.EventStream == nil {
		httpx.WriteError(w, domain.ErrInternal("event stream is not available"))
		return
	}
	h.EventStream.ServeHTTP(w, r)
}

// AdminListSessions handles GET /admin/sessions (super_admin cross-organization view).
func (h *Handlers) AdminListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.Admin.ListAllSessions(r.Context())
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.ListEnvelope(w, sessions, "")
}
