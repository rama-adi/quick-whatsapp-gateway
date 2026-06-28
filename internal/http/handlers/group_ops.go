package handlers

import (
	"context"
	"net/url"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

// decodeParam URL-decodes a path param, mirroring handlers.param: chi routes on
// the raw (escaped) path so WhatsApp JIDs arrive percent-encoded ("@"→"%40").
// Falls back to the raw value if it isn't valid percent-encoding.
func decodeParam(raw string) string {
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

// --- Group request bodies (service is the validator; fields optional on wire) ---

// createGroupInput is POST /sessions/{session}/groups.
type createGroupInput struct {
	Session string `path:"session" doc:"The WhatsApp session id — one attached WhatsApp number. The new group is created on this session's account, which becomes the group's first admin/owner." example:"01HZ0SESSION0000000000000"`
	Body    struct {
		Name         string   `json:"name,omitempty" doc:"The group subject (display name) shown to all members. Required by the service; an empty name is rejected with validation_error." example:"Engineering Team"`
		Participants []string `json:"participants,omitempty" doc:"Initial members to add, given as WhatsApp JIDs (user addresses ending in @s.whatsapp.net). The session's own number is implicitly the owner and need not be listed. Required by the service; numbers that cannot receive group invites may be silently skipped by WhatsApp." example:"[\"6281234567890@s.whatsapp.net\",\"6289876543210@s.whatsapp.net\"]"`
	}
}

// listGroupsInput is GET /sessions/{session}/groups.
type listGroupsInput struct {
	Session string `path:"session" doc:"The WhatsApp session id whose groups to list." example:"01HZ0SESSION0000000000000"`
}

// getGroupInput is GET /sessions/{session}/groups/{gid}.
type getGroupInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that the group belongs to." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"The group's JID (Jabber ID) — WhatsApp's address for the group, always ending in @g.us. Percent-encode the @ as %40 in the URL path; the gateway URL-decodes it." example:"120363041234567890@g.us"`
}

// listGroupMembersInput is GET /sessions/{session}/groups/{gid}/members.
type listGroupMembersInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that the group belongs to." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"The group's JID, ending in @g.us. Percent-encode the @ as %40 in the URL path; the gateway URL-decodes it." example:"120363041234567890@g.us"`
}

// addGroupMembersInput is POST /sessions/{session}/groups/{gid}/members.
type addGroupMembersInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that owns the group; must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"The group's JID, ending in @g.us. Percent-encode the @ as %40 in the URL path; the gateway URL-decodes it." example:"120363041234567890@g.us"`
	Body    struct {
		Participants []string `json:"participants,omitempty" doc:"User JIDs to add to the group (each ending in @s.whatsapp.net). Required by the service. The session's account must be a group admin; some numbers may decline to be added depending on their privacy settings." example:"[\"6281234567890@s.whatsapp.net\"]"`
	}
}

// removeGroupMemberInput is DELETE /sessions/{session}/groups/{gid}/members/{jid}.
type removeGroupMemberInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that owns the group; must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"The group's JID, ending in @g.us. Percent-encode the @ as %40 in the URL path." example:"120363041234567890@g.us"`
	JID     string `path:"jid" doc:"The member's user JID to remove (ending in @s.whatsapp.net). Percent-encode the @ as %40 in the URL path." example:"6281234567890@s.whatsapp.net"`
}

// promoteGroupMemberInput is POST /sessions/{session}/groups/{gid}/members/{jid}/promote.
type promoteGroupMemberInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that owns the group; must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"The group's JID, ending in @g.us. Percent-encode the @ as %40 in the URL path." example:"120363041234567890@g.us"`
	JID     string `path:"jid" doc:"The member's user JID to promote to admin (ending in @s.whatsapp.net). Percent-encode the @ as %40 in the URL path." example:"6281234567890@s.whatsapp.net"`
}

// demoteGroupMemberInput is POST /sessions/{session}/groups/{gid}/members/{jid}/demote.
type demoteGroupMemberInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that owns the group; must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"The group's JID, ending in @g.us. Percent-encode the @ as %40 in the URL path." example:"120363041234567890@g.us"`
	JID     string `path:"jid" doc:"The admin's user JID to demote back to a regular member (ending in @s.whatsapp.net). Percent-encode the @ as %40 in the URL path." example:"6281234567890@s.whatsapp.net"`
}

// updateGroupInput is PATCH /sessions/{session}/groups/{gid}.
type updateGroupInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that owns the group; must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"The group's JID, ending in @g.us. Percent-encode the @ as %40 in the URL path." example:"120363041234567890@g.us"`
	Body    struct {
		Subject     *string `json:"subject,omitempty" doc:"New group subject (display name). Omit to leave unchanged. Sending null is not a valid update — only present fields are applied." example:"Engineering Team (renamed)"`
		Description *string `json:"description,omitempty" doc:"New group description / topic text shown in the group info screen. Omit to leave unchanged." example:"Sprint planning and on-call coordination."`
		Announce    *bool   `json:"announce,omitempty" doc:"Announcement mode. When true, only admins may post messages; when false, all members may post. Omit to leave unchanged." example:"true"`
		Locked      *bool   `json:"locked,omitempty" doc:"Locked (info-edit) mode. When true, only admins may edit the group subject, description, and icon; when false, any member may. Omit to leave unchanged." example:"false"`
	}
}

// getGroupInviteInput is GET /sessions/{session}/groups/{gid}/invite.
type getGroupInviteInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that owns the group; must be connected for this live lookup." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"The group's JID, ending in @g.us. Percent-encode the @ as %40 in the URL path." example:"120363041234567890@g.us"`
}

// revokeGroupInviteInput is DELETE /sessions/{session}/groups/{gid}/invite.
type revokeGroupInviteInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that owns the group; must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"The group's JID, ending in @g.us. Percent-encode the @ as %40 in the URL path." example:"120363041234567890@g.us"`
}

// joinGroupInput is POST /sessions/{session}/groups:join.
type joinGroupInput struct {
	Session string `path:"session" doc:"The WhatsApp session id whose account will join the group; must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	Body    struct {
		Invite string `json:"invite,omitempty" doc:"The group invite code. Accept either the bare code or the full https://chat.whatsapp.com/<code> link. Required by the service. Joining a group that the account is already a member of is effectively a no-op that still returns the group's JID." example:"FaKeInViTeCoDe123"`
	}
}

// leaveGroupInput is POST /sessions/{session}/groups/{gid}:leave.
type leaveGroupInput struct {
	Session string `path:"session" doc:"The WhatsApp session id whose account will leave the group; must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"The group's JID, ending in @g.us. Percent-encode the @ as %40 in the URL path." example:"120363041234567890@g.us"`
}

// approveGroupMembersInput is POST /sessions/{session}/groups/{gid}/members:approve.
type approveGroupMembersInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that owns the group. Note: this endpoint is not implemented in v2 and always returns 501 not_implemented regardless of input." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"The group's JID, ending in @g.us. Not implemented in v2 (always 501)." example:"120363041234567890@g.us"`
	Body    struct {
		Participants []string `json:"participants,omitempty" doc:"User JIDs of pending join requests to approve. Not implemented in v2 (always 501); this field is accepted but never acted on." example:"[\"6281234567890@s.whatsapp.net\"]"`
	}
}

// --- Group outputs ---

type groupInfoOutput struct{ Body domain.GroupInfo }
type groupOutput struct{ Body domain.Group }
type groupListOutput struct{ Body apitypes.List[domain.Group] }
type groupMemberListOutput struct {
	Body apitypes.List[domain.GroupMember]
}

// inviteOutput is the {"invite": link} wire shape from the chi handlers.
type inviteOutput struct {
	Body struct {
		Invite string `json:"invite"`
	}
}

// joinGroupOutput is the {"groupJid": jid} wire shape from the chi handler.
type joinGroupOutput struct {
	Body struct {
		GroupJID string `json:"groupJid"`
	}
}

// RegisterGroupOps registers the group-management operations on the huma API:
// GETs (list/get/members/invite) gated read, all mutations gated send. Code-first
// replacement for the chi groups groups.
func RegisterGroupOps(api huma.API, h *Handlers) {
	read := huma.Middlewares{humax.RequireCap(api, authz.CapRead)}
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "createGroup", Method: "POST", Path: "/api/v1/sessions/{session}/groups",
		Summary: "Create a group", Tags: []string{"Groups"},
		Description: `Creates a new WhatsApp group with the given **name** (group subject) and starting **participants** (a list of user JIDs), and returns the new group's info.

**Capability:** requires ` + "`send`" + `.

**Precondition (live action):** the session must be **connected**. Creating a group talks to WhatsApp in real time; if the session is not connected the gateway responds **501 ` + "`not_implemented`" + `**.

**Side effects:** the group is created on WhatsApp with this session's account as owner/first admin, the listed participants are invited, and the new group is upserted into the gateway's stored WhatsApp data so it appears in ` + "`listGroups`" + ` immediately. Not idempotent — calling twice creates two distinct groups.

**Errors:**
- **400 ` + "`validation_error`" + `** — missing/empty name, no participants, or a malformed JID.
- **404 ` + "`not_found`" + `** — no session with this id is owned by your organization.
- **501 ` + "`not_implemented`" + `** — the session is not connected.`,
		DefaultStatus: 201, Middlewares: send,
	}, func(ctx context.Context, in *createGroupInput) (*groupInfoOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		info, err := h.Groups.Create(ctx, org, in.Session, in.Body.Name, in.Body.Participants)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &groupInfoOutput{Body: info}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "listGroups", Method: "GET", Path: "/api/v1/sessions/{session}/groups",
		Summary: "List a session's groups", Tags: []string{"Groups"},
		Description: `Returns the groups this session belongs to.

**Capability:** requires ` + "`read`" + `.

**Served from stored data:** the list comes from the gateway's stored WhatsApp data, so it works even when the session is **not connected** (no live WhatsApp round-trip).

**Response:** a ` + "`List`" + ` envelope ` + "(`{ \"data\": [...], \"nextCursor\": \"\" }`)" + ` of group objects. The current implementation returns the full set in a single page with an empty ` + "`nextCursor`" + `.

**Errors:**
- **404 ` + "`not_found`" + `** — no session with this id is owned by your organization.`,
		Middlewares: read,
	}, func(ctx context.Context, in *listGroupsInput) (*groupListOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		groups, err := h.Groups.List(ctx, org, in.Session)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &groupListOutput{Body: apitypes.NewList(groups, "")}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getGroup", Method: "GET", Path: "/api/v1/sessions/{session}/groups/{gid}",
		Summary: "Get one group", Tags: []string{"Groups"},
		Description: `Returns the stored details for one group, addressed by its group JID (` + "`gid`" + `): subject, description, and settings.

**Capability:** requires ` + "`read`" + `.

**Served from stored data:** no live WhatsApp connection is needed.

**Errors:**
- **404 ` + "`not_found`" + `** — the session does not exist (or is not yours), or no group with this ` + "`gid`" + ` is stored for it.`,
		Middlewares: read,
	}, func(ctx context.Context, in *getGroupInput) (*groupOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		group, err := h.Groups.Get(ctx, org, in.Session, decodeParam(in.GID))
		if err != nil {
			return nil, humax.Err(err)
		}
		return &groupOutput{Body: group}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "listGroupMembers", Method: "GET", Path: "/api/v1/sessions/{session}/groups/{gid}/members",
		Summary: "List a group's members", Tags: []string{"Groups"},
		Description: `Returns the members of the group, each with their JID and role (member or admin).

**Capability:** requires ` + "`read`" + `.

**Served from stored data:** no live WhatsApp connection is needed.

**Response:** a ` + "`List`" + ` envelope of group-member objects. The current implementation returns all members in a single page with an empty ` + "`nextCursor`" + `.

**Errors:**
- **404 ` + "`not_found`" + `** — the session does not exist (or is not yours), or no group with this ` + "`gid`" + ` is stored for it.`,
		Middlewares: read,
	}, func(ctx context.Context, in *listGroupMembersInput) (*groupMemberListOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		members, err := h.Groups.Members(ctx, org, in.Session, decodeParam(in.GID))
		if err != nil {
			return nil, humax.Err(err)
		}
		return &groupMemberListOutput{Body: apitypes.NewList(members, "")}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "addGroupMembers", Method: "POST", Path: "/api/v1/sessions/{session}/groups/{gid}/members",
		Summary: "Add members to a group", Tags: []string{"Groups"},
		Description: `Adds the listed **participants** (user JIDs) to the group.

**Capability:** requires ` + "`send`" + `.

**Precondition (live action):** the session must be **connected** and its account must be a group **admin**. If the session is not connected the gateway responds **501 ` + "`not_implemented`" + `**.

**Success:** **204 No Content** (empty body).

**Notes:** WhatsApp may decline to add some numbers (privacy settings, invalid number) without failing the whole call. Adding a JID that is already a member is a no-op.

**Errors:**
- **400 ` + "`validation_error`" + `** — empty participant list or a malformed JID.
- **404 ` + "`not_found`" + `** — the session or group does not exist (or is not yours).
- **403 ` + "`forbidden`" + `** — the session's account is not an admin of this group.
- **501 ` + "`not_implemented`" + `** — the session is not connected.`,
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *addGroupMembersInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Groups.AddMembers(ctx, org, in.Session, decodeParam(in.GID), in.Body.Participants); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "removeGroupMember", Method: "DELETE", Path: "/api/v1/sessions/{session}/groups/{gid}/members/{jid}",
		Summary: "Remove a member from a group", Tags: []string{"Groups"},
		Description: `Removes the member named by ` + "`jid`" + ` from the group.

**Capability:** requires ` + "`send`" + `.

**Precondition (live action):** the session must be **connected** and its account must be a group **admin**. If the session is not connected the gateway responds **501 ` + "`not_implemented`" + `**.

**Success:** **204 No Content**. Idempotent in effect — removing someone who is not (or no longer) a member still ends with them not in the group.

**Errors:**
- **404 ` + "`not_found`" + `** — the session or group does not exist (or is not yours).
- **403 ` + "`forbidden`" + `** — the session's account is not an admin of this group.
- **501 ` + "`not_implemented`" + `** — the session is not connected.`,
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *removeGroupMemberInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Groups.RemoveMember(ctx, org, in.Session, decodeParam(in.GID), decodeParam(in.JID)); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "promoteGroupMember", Method: "POST", Path: "/api/v1/sessions/{session}/groups/{gid}/members/{jid}/promote",
		Summary: "Promote a member to admin", Tags: []string{"Groups"},
		Description: `Makes the member named by ` + "`jid`" + ` a group **admin**.

**Capability:** requires ` + "`send`" + `.

**Precondition (live action):** the session must be **connected** and its account must already be a group **admin**. If the session is not connected the gateway responds **501 ` + "`not_implemented`" + `**.

**Success:** **204 No Content**. Promoting someone who is already an admin is a no-op.

**Errors:**
- **404 ` + "`not_found`" + `** — the session, group, or member does not exist (or is not yours).
- **403 ` + "`forbidden`" + `** — the session's account is not an admin of this group.
- **501 ` + "`not_implemented`" + `** — the session is not connected.`,
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *promoteGroupMemberInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Groups.Promote(ctx, org, in.Session, decodeParam(in.GID), decodeParam(in.JID)); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "demoteGroupMember", Method: "POST", Path: "/api/v1/sessions/{session}/groups/{gid}/members/{jid}/demote",
		Summary: "Demote an admin to member", Tags: []string{"Groups"},
		Description: `Removes admin rights from the member named by ` + "`jid`" + `, making them a regular member.

**Capability:** requires ` + "`send`" + `.

**Precondition (live action):** the session must be **connected** and its account must be a group **admin**. If the session is not connected the gateway responds **501 ` + "`not_implemented`" + `**.

**Success:** **204 No Content**. Demoting someone who is already a regular member is a no-op.

**Errors:**
- **404 ` + "`not_found`" + `** — the session, group, or member does not exist (or is not yours).
- **403 ` + "`forbidden`" + `** — the session's account is not an admin of this group.
- **501 ` + "`not_implemented`" + `** — the session is not connected.`,
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *demoteGroupMemberInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Groups.Demote(ctx, org, in.Session, decodeParam(in.GID), decodeParam(in.JID)); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "updateGroup", Method: "PATCH", Path: "/api/v1/sessions/{session}/groups/{gid}",
		Summary: "Update group settings", Tags: []string{"Groups"},
		Description: `Changes the group's settings — **subject**, **description**, whether only admins can post (**announce**), and whether only admins can edit group info (**locked**).

**Partial update:** send only the fields you want to change; omitted fields are left untouched. Each field maps to a separate WhatsApp operation, applied in order.

**Capability:** requires ` + "`send`" + `.

**Precondition (live action):** the session must be **connected** and (for most settings) its account must be a group **admin**. If the session is not connected the gateway responds **501 ` + "`not_implemented`" + `**.

**Success:** **204 No Content**.

**Errors:**
- **400 ` + "`validation_error`" + `** — malformed body.
- **404 ` + "`not_found`" + `** — the session or group does not exist (or is not yours).
- **403 ` + "`forbidden`" + `** — the session's account lacks the rights to change this setting.
- **501 ` + "`not_implemented`" + `** — the session is not connected.`,
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *updateGroupInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		err = h.Groups.UpdateSettings(ctx, org, in.Session, decodeParam(in.GID), domain.GroupSettings{
			Subject:     in.Body.Subject,
			Description: in.Body.Description,
			Announce:    in.Body.Announce,
			Locked:      in.Body.Locked,
		})
		if err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getGroupInvite", Method: "GET", Path: "/api/v1/sessions/{session}/groups/{gid}/invite",
		Summary: "Get the group invite link", Tags: []string{"Groups"},
		Description: `Returns the group's current invite link (a ` + "`https://chat.whatsapp.com/<code>`" + ` URL anyone can use to join), in the shape ` + "`{ \"invite\": \"https://chat.whatsapp.com/...\" }`" + `.

**Capability:** requires ` + "`read`" + `.

**Precondition (live lookup):** unlike most read endpoints this is a **live** WhatsApp query, so the session must be **connected**; if it is not the gateway responds **501 ` + "`not_implemented`" + `**. The session's account must also be a group admin to fetch the link.

**Errors:**
- **404 ` + "`not_found`" + `** — the session or group does not exist (or is not yours).
- **403 ` + "`forbidden`" + `** — the session's account is not an admin of this group.
- **501 ` + "`not_implemented`" + `** — the session is not connected.`,
		Middlewares: read,
	}, func(ctx context.Context, in *getGroupInviteInput) (*inviteOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		link, err := h.Groups.InviteLink(ctx, org, in.Session, decodeParam(in.GID))
		if err != nil {
			return nil, humax.Err(err)
		}
		out := &inviteOutput{}
		out.Body.Invite = link
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revokeGroupInvite", Method: "DELETE", Path: "/api/v1/sessions/{session}/groups/{gid}/invite",
		Summary: "Reset the group invite link", Tags: []string{"Groups"},
		Description: `Cancels the current invite link and generates a new one, then returns it in the shape ` + "`{ \"invite\": \"https://chat.whatsapp.com/...\" }`" + `.

**Side effect:** any previously shared link **stops working** immediately — share the returned link going forward.

**Capability:** requires ` + "`send`" + `.

**Precondition (live action):** the session must be **connected** and its account must be a group **admin**. If the session is not connected the gateway responds **501 ` + "`not_implemented`" + `**.

**Errors:**
- **404 ` + "`not_found`" + `** — the session or group does not exist (or is not yours).
- **403 ` + "`forbidden`" + `** — the session's account is not an admin of this group.
- **501 ` + "`not_implemented`" + `** — the session is not connected.`,
		Middlewares: send,
	}, func(ctx context.Context, in *revokeGroupInviteInput) (*inviteOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		link, err := h.Groups.RevokeInvite(ctx, org, in.Session, decodeParam(in.GID))
		if err != nil {
			return nil, humax.Err(err)
		}
		out := &inviteOutput{}
		out.Body.Invite = link
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "joinGroup", Method: "POST", Path: "/api/v1/sessions/{session}/groups:join",
		Summary: "Join a group by invite", Tags: []string{"Groups"},
		Description: `Joins the group named by the **invite** code (the code from a WhatsApp group invite link) and returns the joined group's JID in the shape ` + "`{ \"groupJid\": \"...@g.us\" }`" + `.

**Capability:** requires ` + "`send`" + `.

**Precondition (live action):** the session must be **connected**. If it is not the gateway responds **501 ` + "`not_implemented`" + `**.

**Success:** **200 OK** with the group's JID. Use that JID with the other group endpoints (e.g. ` + "`getGroup`" + `, ` + "`listGroupMembers`" + `). Joining a group the account already belongs to still returns the group's JID.

**Errors:**
- **400 ` + "`validation_error`" + `** — missing or unparseable invite code/link, or the invite is invalid/expired.
- **404 ` + "`not_found`" + `** — no session with this id is owned by your organization.
- **501 ` + "`not_implemented`" + `** — the session is not connected.`,
		Middlewares: send,
	}, func(ctx context.Context, in *joinGroupInput) (*joinGroupOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		jid, err := h.Groups.Join(ctx, org, in.Session, in.Body.Invite)
		if err != nil {
			return nil, humax.Err(err)
		}
		out := &joinGroupOutput{}
		out.Body.GroupJID = jid
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "leaveGroup", Method: "POST", Path: "/api/v1/sessions/{session}/groups/{gid}:leave",
		Summary: "Leave a group", Tags: []string{"Groups"},
		Description: `Removes the session's **own** account from the group.

**Capability:** requires ` + "`send`" + `.

**Precondition (live action):** the session must be **connected**. If it is not the gateway responds **501 ` + "`not_implemented`" + `**.

**Success:** **204 No Content**. After leaving, this session can no longer act on the group; reading still works only on whatever stored data remains.

**Errors:**
- **404 ` + "`not_found`" + `** — the session or group does not exist (or is not yours).
- **501 ` + "`not_implemented`" + `** — the session is not connected.`,
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *leaveGroupInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Groups.Leave(ctx, org, in.Session, decodeParam(in.GID)); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "approveGroupMembers", Method: "POST", Path: "/api/v1/sessions/{session}/groups/{gid}/members:approve",
		Summary: "Approve pending join requests (not implemented in v2)", Tags: []string{"Groups"},
		Description: `**Not implemented in v2.** Approving people waiting to join a group is not part of v2's live WhatsApp surface, so this endpoint **always responds 501 ` + "`not_implemented`" + `** regardless of the request body. The route and ` + "`participants`" + ` body shape are reserved for a future release.

**Capability:** requires ` + "`send`" + ` (checked before the 501 is returned).`,
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *approveGroupMembersInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Groups.ApproveMembers(ctx, org, in.Session, decodeParam(in.GID), in.Body.Participants); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})
}
