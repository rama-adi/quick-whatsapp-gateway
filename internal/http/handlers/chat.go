package handlers

import (
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
)

// ListChats handles GET /sessions/{session}/chats.
func (h *Handlers) ListChats(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	limit, cursor := httpx.ParsePage(r)
	page, err := h.Chats.List(r.Context(), organizationID, param(r, "session"), cursor, limit)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.ListEnvelope(w, page.Items, page.NextCursor)
}

// GetChat handles GET /sessions/{session}/chats/{cid}.
func (h *Handlers) GetChat(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	chat, err := h.Chats.Get(r.Context(), organizationID, param(r, "session"), param(r, "cid"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, chat)
}

// ListChatMessages handles GET /sessions/{session}/chats/{cid}/messages.
func (h *Handlers) ListChatMessages(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	limit, cursor := httpx.ParsePage(r)
	page, err := h.Chats.ListMessages(r.Context(), organizationID, param(r, "session"), param(r, "cid"), cursor, limit)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.ListEnvelope(w, page.Items, page.NextCursor)
}

// ReadChat handles POST /sessions/{session}/chats/{cid}/read.
func (h *Handlers) ReadChat(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	chat, err := h.Chats.Read(r.Context(), organizationID, param(r, "session"), param(r, "cid"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, chat)
}

// updateChatBody is the PATCH /chats/{cid} request (archive/pin/mute).
type updateChatBody struct {
	Archived   *bool  `json:"archived,omitempty"`
	Pinned     *bool  `json:"pinned,omitempty"`
	MutedUntil *int64 `json:"mutedUntil,omitempty"`
	Unmute     bool   `json:"unmute,omitempty"`
}

// UpdateChat handles PATCH /sessions/{session}/chats/{cid}.
func (h *Handlers) UpdateChat(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body updateChatBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	chat, err := h.Chats.Update(r.Context(), organizationID, param(r, "session"), param(r, "cid"), service.ChatUpdate{
		Archived:   body.Archived,
		Pinned:     body.Pinned,
		MutedUntil: body.MutedUntil,
		Unmute:     body.Unmute,
	})
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, chat)
}

// DeleteChat handles DELETE /sessions/{session}/chats/{cid}.
func (h *Handlers) DeleteChat(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	if err := h.Chats.Delete(r.Context(), organizationID, param(r, "session"), param(r, "cid")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// chatPresenceBody is the PUT /chats/{cid}/presence request.
type chatPresenceBody struct {
	State string `json:"state"`
}

// ChatPresence handles PUT /sessions/{session}/chats/{cid}/presence.
func (h *Handlers) ChatPresence(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body chatPresenceBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if err := h.Chats.SetPresence(r.Context(), organizationID, param(r, "session"), param(r, "cid"), body.State); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
