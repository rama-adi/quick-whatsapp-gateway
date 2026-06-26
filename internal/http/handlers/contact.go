package handlers

import (
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// ListContacts handles GET /sessions/{session}/contacts (the "found users"
// feature). Filters: ?source=dm|group, ?group={jid}, ?q=.
func (h *Handlers) ListContacts(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	limit, cursor := httpx.ParsePage(r)
	q := r.URL.Query()
	f := store.ContactFilter{
		Source:   q.Get("source"),
		GroupJID: q.Get("group"),
		Q:        q.Get("q"),
	}
	page, err := h.Contacts.List(r.Context(), organizationID, param(r, "session"), f, cursor, limit)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.ListEnvelope(w, page.Items, page.NextCursor)
}

// GetContact handles GET /sessions/{session}/contacts/{lid} — identity + DM +
// per-group memberships.
func (h *Handlers) GetContact(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	detail, err := h.Contacts.Get(r.Context(), organizationID, param(r, "session"), param(r, "lid"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

// CheckContact handles GET /sessions/{session}/contacts/check?phone=.
func (h *Handlers) CheckContact(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	res, err := h.Contacts.Check(r.Context(), organizationID, param(r, "session"), r.URL.Query().Get("phone"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// ContactPicture handles GET /sessions/{session}/contacts/{jid}/picture.
func (h *Handlers) ContactPicture(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	pic, err := h.Contacts.Picture(r.Context(), organizationID, param(r, "session"), param(r, "jid"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pic)
}

// ContactAbout handles GET /sessions/{session}/contacts/{jid}/about.
func (h *Handlers) ContactAbout(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	about, err := h.Contacts.About(r.Context(), organizationID, param(r, "session"), param(r, "jid"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"about": about})
}

// BlockContact handles POST /sessions/{session}/contacts/{jid}/block.
func (h *Handlers) BlockContact(w http.ResponseWriter, r *http.Request) {
	h.setBlocked(w, r, true)
}

// UnblockContact handles POST /sessions/{session}/contacts/{jid}/unblock.
func (h *Handlers) UnblockContact(w http.ResponseWriter, r *http.Request) {
	h.setBlocked(w, r, false)
}

func (h *Handlers) setBlocked(w http.ResponseWriter, r *http.Request, blocked bool) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	if err := h.Contacts.SetBlocked(r.Context(), organizationID, param(r, "session"), param(r, "jid"), blocked); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
