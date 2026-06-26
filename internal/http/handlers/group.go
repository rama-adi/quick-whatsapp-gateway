package handlers

import (
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// createGroupBody is the POST /groups request.
type createGroupBody struct {
	Name         string   `json:"name"`
	Participants []string `json:"participants,omitempty"`
}

// CreateGroup handles POST /sessions/{session}/groups.
func (h *Handlers) CreateGroup(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body createGroupBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	info, err := h.Groups.Create(r.Context(), organizationID, param(r, "session"), body.Name, body.Participants)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, info)
}

// ListGroups handles GET /sessions/{session}/groups.
func (h *Handlers) ListGroups(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	groups, err := h.Groups.List(r.Context(), organizationID, param(r, "session"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.ListEnvelope(w, groups, "")
}

// GetGroup handles GET /sessions/{session}/groups/{gid}.
func (h *Handlers) GetGroup(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	group, err := h.Groups.Get(r.Context(), organizationID, param(r, "session"), param(r, "gid"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, group)
}

// ListGroupMembers handles GET /sessions/{session}/groups/{gid}/members.
func (h *Handlers) ListGroupMembers(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	members, err := h.Groups.Members(r.Context(), organizationID, param(r, "session"), param(r, "gid"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.ListEnvelope(w, members, "")
}

// addMembersBody is the POST /groups/{gid}/members request.
type addMembersBody struct {
	Participants []string `json:"participants"`
}

// AddGroupMembers handles POST /sessions/{session}/groups/{gid}/members.
func (h *Handlers) AddGroupMembers(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body addMembersBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if err := h.Groups.AddMembers(r.Context(), organizationID, param(r, "session"), param(r, "gid"), body.Participants); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveGroupMember handles DELETE /sessions/{session}/groups/{gid}/members/{jid}.
func (h *Handlers) RemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	if err := h.Groups.RemoveMember(r.Context(), organizationID, param(r, "session"), param(r, "gid"), param(r, "jid")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PromoteGroupMember handles POST /sessions/{session}/groups/{gid}/members/{jid}/promote.
func (h *Handlers) PromoteGroupMember(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	if err := h.Groups.Promote(r.Context(), organizationID, param(r, "session"), param(r, "gid"), param(r, "jid")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DemoteGroupMember handles POST /sessions/{session}/groups/{gid}/members/{jid}/demote.
func (h *Handlers) DemoteGroupMember(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	if err := h.Groups.Demote(r.Context(), organizationID, param(r, "session"), param(r, "gid"), param(r, "jid")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// updateGroupBody is the PATCH /groups/{gid} request (subject/desc/announce/locked).
type updateGroupBody struct {
	Subject     *string `json:"subject,omitempty"`
	Description *string `json:"description,omitempty"`
	Announce    *bool   `json:"announce,omitempty"`
	Locked      *bool   `json:"locked,omitempty"`
}

// UpdateGroup handles PATCH /sessions/{session}/groups/{gid}.
func (h *Handlers) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body updateGroupBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	err := h.Groups.UpdateSettings(r.Context(), organizationID, param(r, "session"), param(r, "gid"), domain.GroupSettings{
		Subject:     body.Subject,
		Description: body.Description,
		Announce:    body.Announce,
		Locked:      body.Locked,
	})
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetGroupInvite handles GET /sessions/{session}/groups/{gid}/invite.
func (h *Handlers) GetGroupInvite(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	link, err := h.Groups.InviteLink(r.Context(), organizationID, param(r, "session"), param(r, "gid"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"invite": link})
}

// RevokeGroupInvite handles DELETE /sessions/{session}/groups/{gid}/invite.
func (h *Handlers) RevokeGroupInvite(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	link, err := h.Groups.RevokeInvite(r.Context(), organizationID, param(r, "session"), param(r, "gid"))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"invite": link})
}

// joinGroupBody is the POST /groups:join request.
type joinGroupBody struct {
	Invite string `json:"invite"`
}

// JoinGroup handles POST /sessions/{session}/groups:join.
func (h *Handlers) JoinGroup(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body joinGroupBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	jid, err := h.Groups.Join(r.Context(), organizationID, param(r, "session"), body.Invite)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"groupJid": jid})
}

// LeaveGroup handles POST /sessions/{session}/groups/{gid}:leave.
func (h *Handlers) LeaveGroup(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	if err := h.Groups.Leave(r.Context(), organizationID, param(r, "session"), param(r, "gid")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// approveMembersBody is the POST /groups/{gid}/members:approve request.
type approveMembersBody struct {
	Participants []string `json:"participants"`
}

// ApproveGroupMembers handles POST /sessions/{session}/groups/{gid}/members:approve.
func (h *Handlers) ApproveGroupMembers(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	var body approveMembersBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if err := h.Groups.ApproveMembers(r.Context(), organizationID, param(r, "session"), param(r, "gid"), body.Participants); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
