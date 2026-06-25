package handlers

import (
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
)

// createKeyBody is the POST /keys request.
type createKeyBody struct {
	Name        string             `json:"name"`
	Permissions domain.Permissions `json:"permissions"`
	Scope       domain.APIKeyScope `json:"scope,omitempty"`
	ExpiresAt   *int64             `json:"expiresAt,omitempty"`
}

// CreateKey handles POST /keys. The full key is returned exactly once.
func (h *Handlers) CreateKey(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	var body createKeyBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	res, err := h.Keys.Create(r.Context(), tenantID, service.CreateKeyInput{
		Name:        body.Name,
		Permissions: body.Permissions,
		Scope:       body.Scope,
		ExpiresAt:   body.ExpiresAt,
	})
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, res)
}

// ListKeys handles GET /keys.
func (h *Handlers) ListKeys(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	keys, err := h.Keys.List(r.Context(), tenantID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.ListEnvelope(w, keys, "")
}

// GetKey handles GET /keys/{id}.
func (h *Handlers) GetKey(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	key, err := h.Keys.Get(r.Context(), tenantID, param(r, "id"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, key)
}

// DeleteKey handles DELETE /keys/{id} (revoke).
func (h *Handlers) DeleteKey(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	if err := h.Keys.Delete(r.Context(), tenantID, param(r, "id")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RotateKey handles POST /keys/{id}:rotate. Returns the new full key once.
func (h *Handlers) RotateKey(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	res, err := h.Keys.Rotate(r.Context(), tenantID, param(r, "id"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
