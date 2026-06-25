package handlers

import (
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
)

// webhookBody is the POST/PATCH /webhooks request. Secret is the plaintext HMAC
// secret; it is encrypted at rest and never returned.
type webhookBody struct {
	SessionID     *string             `json:"sessionId,omitempty"`
	URL           string              `json:"url,omitempty"`
	Events        []string            `json:"events,omitempty"`
	Secret        *string             `json:"secret,omitempty"`
	CustomHeaders map[string]string   `json:"customHeaders,omitempty"`
	RetryPolicy   *domain.RetryPolicy `json:"retryPolicy,omitempty"`
	Active        *bool               `json:"active,omitempty"`
}

func (b webhookBody) toInput() service.WebhookInput {
	return service.WebhookInput{
		SessionID:     b.SessionID,
		URL:           b.URL,
		Events:        b.Events,
		Secret:        b.Secret,
		CustomHeaders: b.CustomHeaders,
		RetryPolicy:   b.RetryPolicy,
		Active:        b.Active,
	}
}

// CreateWebhook handles POST /webhooks.
func (h *Handlers) CreateWebhook(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	var body webhookBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	hook, err := h.Webhooks.Create(r.Context(), tenantID, body.toInput())
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, hook)
}

// ListWebhooks handles GET /webhooks.
func (h *Handlers) ListWebhooks(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	hooks, err := h.Webhooks.List(r.Context(), tenantID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.ListEnvelope(w, hooks, "")
}

// GetWebhook handles GET /webhooks/{id}.
func (h *Handlers) GetWebhook(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	hook, err := h.Webhooks.Get(r.Context(), tenantID, param(r, "id"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, hook)
}

// UpdateWebhook handles PATCH /webhooks/{id}.
func (h *Handlers) UpdateWebhook(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	var body webhookBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	hook, err := h.Webhooks.Update(r.Context(), tenantID, param(r, "id"), body.toInput())
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, hook)
}

// DeleteWebhook handles DELETE /webhooks/{id}.
func (h *Handlers) DeleteWebhook(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	if err := h.Webhooks.Delete(r.Context(), tenantID, param(r, "id")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
