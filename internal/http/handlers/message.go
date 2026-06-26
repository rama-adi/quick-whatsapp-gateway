package handlers

import (
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// idempotencyHeader is the §8 replay key header.
const idempotencyHeader = "Idempotency-Key"

// SendMessage handles POST /sessions/{id}/messages. The body is the discriminated
// domain.SendRequest; Idempotency-Key (header) and ?async (query) tune delivery.
func (h *Handlers) SendMessage(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var req domain.SendRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	opts := outbound.SendOptions{
		Async:          r.URL.Query().Has("async"),
		IdempotencyKey: r.Header.Get(idempotencyHeader),
	}
	res, err := h.Messages.Send(r.Context(), organizationID, param(r, "session"), req, opts)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	status := http.StatusOK
	if res.Mode == outbound.ModeAsync {
		status = http.StatusAccepted
	}
	httpx.WriteJSON(w, status, res)
}

// editMessageBody is the PATCH /messages/{mid} request. chat identifies the chat
// the target message lives in.
type editMessageBody struct {
	Chat string `json:"chat"`
	Text string `json:"text"`
}

// EditMessage handles PATCH /sessions/{id}/messages/{mid}.
func (h *Handlers) EditMessage(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body editMessageBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	res, err := h.Messages.Edit(r.Context(), organizationID, param(r, "session"), body.Chat, param(r, "mid"), body.Text)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// revokeMessageBody is the DELETE /messages/{mid} request. sender is the original
// sender JID ("" for your own message).
type revokeMessageBody struct {
	Chat   string `json:"chat"`
	Sender string `json:"sender,omitempty"`
}

// RevokeMessage handles DELETE /sessions/{id}/messages/{mid}.
func (h *Handlers) RevokeMessage(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body revokeMessageBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	res, err := h.Messages.Revoke(r.Context(), organizationID, param(r, "session"), body.Chat, body.Sender, param(r, "mid"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// reactionBody is the POST /messages/{mid}/reaction request.
type reactionBody struct {
	Chat   string `json:"chat"`
	Sender string `json:"sender,omitempty"`
	Emoji  string `json:"emoji"`
}

// AddReaction handles POST /sessions/{id}/messages/{mid}/reaction.
func (h *Handlers) AddReaction(w http.ResponseWriter, r *http.Request) {
	h.reaction(w, r, false)
}

// RemoveReaction handles DELETE /sessions/{id}/messages/{mid}/reaction.
func (h *Handlers) RemoveReaction(w http.ResponseWriter, r *http.Request) {
	h.reaction(w, r, true)
}

func (h *Handlers) reaction(w http.ResponseWriter, r *http.Request, remove bool) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body reactionBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	emoji := body.Emoji
	if remove {
		emoji = "" // empty emoji removes the reaction
	}
	res, err := h.Messages.React(r.Context(), organizationID, param(r, "session"), body.Chat, body.Sender, param(r, "mid"), emoji)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// forwardBody is the POST /messages/{mid}/forward request.
type forwardBody struct {
	Chat   string `json:"chat"`
	Sender string `json:"sender,omitempty"`
	To     string `json:"to"`
}

// ForwardMessage handles POST /sessions/{id}/messages/{mid}/forward.
func (h *Handlers) ForwardMessage(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body forwardBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	res, err := h.Messages.Forward(r.Context(), organizationID, param(r, "session"), body.Chat, body.Sender, param(r, "mid"), body.To)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// voteBody is the POST /messages/{mid}/vote request.
type voteBody struct {
	Chat    string   `json:"chat"`
	Sender  string   `json:"sender,omitempty"`
	Options []string `json:"options"`
}

// VoteMessage handles POST /sessions/{id}/messages/{mid}/vote.
func (h *Handlers) VoteMessage(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body voteBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	res, err := h.Messages.Vote(r.Context(), organizationID, param(r, "session"), body.Chat, body.Sender, param(r, "mid"), body.Options)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
