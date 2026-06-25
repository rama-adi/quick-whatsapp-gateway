package handlers

import (
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// --- Channels ---

// createChannelBody is the POST /channels request.
type createChannelBody struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CreateChannel handles POST /sessions/{session}/channels.
func (h *Handlers) CreateChannel(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	var body createChannelBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	jid, err := h.Channels.Create(r.Context(), tenantID, param(r, "session"), body.Name, body.Description)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"jid": jid})
}

// FollowChannel handles POST /sessions/{session}/channels/{jid}:follow.
func (h *Handlers) FollowChannel(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	if err := h.Channels.Follow(r.Context(), tenantID, param(r, "session"), param(r, "jid")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UnfollowChannel handles POST /sessions/{session}/channels/{jid}:unfollow.
func (h *Handlers) UnfollowChannel(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	if err := h.Channels.Unfollow(r.Context(), tenantID, param(r, "session"), param(r, "jid")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// muteChannelBody is the POST /channels/{jid}:mute request.
type muteChannelBody struct {
	Mute *bool `json:"mute,omitempty"`
}

// MuteChannel handles POST /sessions/{session}/channels/{jid}:mute.
func (h *Handlers) MuteChannel(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	var body muteChannelBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	mute := true // default to muting; pass {"mute":false} to unmute
	if body.Mute != nil {
		mute = *body.Mute
	}
	if err := h.Channels.Mute(r.Context(), tenantID, param(r, "session"), param(r, "jid"), mute); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListChannelMessages handles GET /sessions/{session}/channels/{jid}/messages.
func (h *Handlers) ListChannelMessages(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	limit, cursor := httpx.ParsePage(r)
	page, err := h.Channels.Messages(r.Context(), tenantID, param(r, "session"), param(r, "jid"), cursor, limit)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.ListEnvelope(w, page.Items, page.NextCursor)
}

// --- Status ---

// postStatusBody is the POST /status request. Only type "text" is supported in
// v1; type "image" (and any other media) returns not_implemented (501).
type postStatusBody struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

// PostStatus handles POST /sessions/{session}/status.
func (h *Handlers) PostStatus(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	var body postStatusBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	switch body.Type {
	case "", "text":
		id, err := h.Status.PostText(r.Context(), tenantID, param(r, "session"), body.Text)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"messageId": id})
	default:
		// image / video / any media status is 501 in v1, consistent with media send.
		httpx.WriteError(w, domain.ErrNotImplemented(body.Type+" status is not implemented yet"))
	}
}

// --- Presence ---

// presenceBody is the PUT /presence request.
type presenceBody struct {
	State string `json:"state"`
}

// SetPresence handles PUT /sessions/{session}/presence.
func (h *Handlers) SetPresence(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenant(w, r)
	if !ok {
		return
	}
	var body presenceBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if err := h.Presence.Set(r.Context(), tenantID, param(r, "session"), body.State); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
