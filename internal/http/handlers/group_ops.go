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

// decodeParam URL-decodes a path param for group JIDs and member JIDs.
func decodeParam(raw string) string {
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

// createGroupInput is POST /sessions/{session}/groups.
type createGroupInput struct {
	Session string `path:"session" doc:"WhatsApp session id that creates the group." example:"01HZ0SESSION0000000000000"`
	Body    struct {
		Name         string   `json:"name,omitempty" doc:"Group subject. Required." example:"Engineering Team"`
		Participants []string `json:"participants,omitempty" doc:"Initial member JIDs." example:"[\"6281234567890@s.whatsapp.net\",\"6289876543210@s.whatsapp.net\"]"`
	}
}

// listGroupsInput is GET /sessions/{session}/groups.
type listGroupsInput struct {
	Session string `path:"session" doc:"WhatsApp session id to read groups from." example:"01HZ0SESSION0000000000000"`
}

// getGroupInput is GET /sessions/{session}/groups/{gid}.
type getGroupInput struct {
	Session string `path:"session" doc:"WhatsApp session id that owns the group." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"Group JID, usually ending in @g.us. Percent-encode reserved characters such as @ in the URL path." example:"120363041234567890@g.us"`
}

// listGroupMembersInput is GET /sessions/{session}/groups/{gid}/members.
type listGroupMembersInput struct {
	Session string `path:"session" doc:"WhatsApp session id that owns the group." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"Group JID, usually ending in @g.us. Percent-encode reserved characters such as @ in the URL path." example:"120363041234567890@g.us"`
}

// addGroupMembersInput is POST /sessions/{session}/groups/{gid}/members.
type addGroupMembersInput struct {
	Session string `path:"session" doc:"WhatsApp session id that owns the group. Must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"Group JID, usually ending in @g.us. Percent-encode reserved characters such as @ in the URL path." example:"120363041234567890@g.us"`
	Body    struct {
		Participants []string `json:"participants,omitempty" doc:"User JIDs to add." example:"[\"6281234567890@s.whatsapp.net\"]"`
	}
}

// removeGroupMemberInput is DELETE /sessions/{session}/groups/{gid}/members/{jid}.
type removeGroupMemberInput struct {
	Session string `path:"session" doc:"WhatsApp session id that owns the group. Must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"Group JID, ending in @g.us." example:"120363041234567890@g.us"`
	JID     string `path:"jid" doc:"Member JID to remove, ending in @s.whatsapp.net." example:"6281234567890@s.whatsapp.net"`
}

// promoteGroupMemberInput is POST /sessions/{session}/groups/{gid}/members/{jid}/promote.
type promoteGroupMemberInput struct {
	Session string `path:"session" doc:"WhatsApp session id that owns the group. Must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"Group JID." example:"120363041234567890@g.us"`
	JID     string `path:"jid" doc:"Member JID to promote." example:"6281234567890@s.whatsapp.net"`
}

// demoteGroupMemberInput is POST /sessions/{session}/groups/{gid}/members/{jid}/demote.
type demoteGroupMemberInput struct {
	Session string `path:"session" doc:"WhatsApp session id that owns the group. Must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"Group JID." example:"120363041234567890@g.us"`
	JID     string `path:"jid" doc:"Admin JID to demote." example:"6281234567890@s.whatsapp.net"`
}

// updateGroupInput is PATCH /sessions/{session}/groups/{gid}.
type updateGroupInput struct {
	Session string `path:"session" doc:"WhatsApp session id that owns the group. Must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"Group JID." example:"120363041234567890@g.us"`
	Body    struct {
		Subject     *string `json:"subject,omitempty" doc:"New subject." example:"Engineering Team (renamed)"`
		Description *string `json:"description,omitempty" doc:"New description." example:"Sprint planning and on-call coordination."`
		Announce    *bool   `json:"announce,omitempty" doc:"When true, only admins can post." example:"true"`
		Locked      *bool   `json:"locked,omitempty" doc:"When true, only admins can edit group info." example:"false"`
	}
}

// getGroupInviteInput is GET /sessions/{session}/groups/{gid}/invite.
type getGroupInviteInput struct {
	Session string `path:"session" doc:"WhatsApp session id that owns the group. Must be connected for this live lookup." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"Group JID." example:"120363041234567890@g.us"`
}

// revokeGroupInviteInput is DELETE /sessions/{session}/groups/{gid}/invite.
type revokeGroupInviteInput struct {
	Session string `path:"session" doc:"WhatsApp session id that owns the group. Must be connected for this live action." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"Group JID." example:"120363041234567890@g.us"`
}

// joinGroupInput is POST /sessions/{session}/groups:join.
type joinGroupInput struct {
	Session string `path:"session" doc:"WhatsApp session id that will join the group. Must be connected." example:"01HZ0SESSION0000000000000"`
	Body    struct {
		Invite string `json:"invite,omitempty" doc:"Invite code or full WhatsApp chat link." example:"FaKeInViTeCoDe123"`
	}
}

// leaveGroupInput is POST /sessions/{session}/groups/{gid}:leave.
type leaveGroupInput struct {
	Session string `path:"session" doc:"WhatsApp session id that will leave the group. Must be connected." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"Group JID." example:"120363041234567890@g.us"`
}

// approveGroupMembersInput is POST /sessions/{session}/groups/{gid}/members:approve.
type approveGroupMembersInput struct {
	Session string `path:"session" doc:"Not implemented in v2. Endpoint exists for compatibility." example:"01HZ0SESSION0000000000000"`
	GID     string `path:"gid" doc:"Group JID." example:"120363041234567890@g.us"`
	Body    struct {
		Participants []string `json:"participants,omitempty" doc:"Reserved for future implementation." example:"[\"6281234567890@s.whatsapp.net\"]"`
	}
}

type groupInfoOutput struct{ Body domain.GroupInfo }
type groupOutput struct{ Body domain.Group }
type groupListOutput struct{ Body apitypes.List[domain.Group] }
type groupMemberListOutput struct {
	Body apitypes.List[domain.GroupMember]
}

type inviteOutput struct {
	Body struct {
		Invite string `json:"invite"`
	}
}

type joinGroupOutput struct {
	Body struct {
		GroupJID string `json:"groupJid"`
	}
}

// RegisterGroupOps registers group-management operations.
func RegisterGroupOps(api huma.API, h *Handlers) {
	read := huma.Middlewares{humax.RequireCap(api, authz.CapRead)}
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "createGroup", Method: "POST", Path: "/api/v1/sessions/{session}/groups",
		Summary: "Create a group", Tags: []string{"Groups"},
		Description: "Create a WhatsApp group for the session.\n\n" +
			"Creates on WhatsApp and stores group info locally.\n\n" +
			"Errors: `validation_error`, `not_found`, `not_implemented` if session is disconnected.",
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
		Description: "List groups from stored data for one session.\n\n" +
			"Returns all groups for the session.",
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
		Description: "Return stored group details by `gid`.",
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
		Description: "Return stored members for one group.",
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
		Description: "Add member JIDs to a group.\n\n" +
			"Requires `send`, a connected session, and admin rights in the group. Returns 204 on success.\n\n" +
			"Errors: `validation_error`, `not_found`, `forbidden`, `not_implemented`.",
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
		Description: "Remove a member by JID. Idempotent when member is already absent.\n\n" +
			"Requires session admin privileges. Returns 204.",
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
		Description: "Set one member to admin. No change if member is already admin.\n" +
			"Requires `send`, a connected session, and admin rights in the group. Returns 204.\n\n" +
			"Errors: `not_found`, `forbidden`, `not_implemented`.",
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
		Description: "Demote one admin to regular member. No change if already member.\n" +
			"Requires `send`, a connected session, and admin rights in the group. Returns 204.\n\n" +
			"Errors: `not_found`, `forbidden`, `not_implemented`.",
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
		Description: "Partial update for subject, description, announce mode, and locked mode.\n\n" +
			"Requires `send`, a connected session, and enough WhatsApp rights to change the setting.\n\n" +
			"Errors: `validation_error`, `not_found`, `forbidden`, `not_implemented`.",
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
		Description: "Live lookup of group's current invite URL.\n\n" +
			"Requires `read`, a connected session, and admin rights in the group. Returns `{\"invite\": \"https://chat.whatsapp.com/...\"}`.\n\n" +
			"Errors: `not_found`, `forbidden`, `not_implemented`.",
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
		Description: "Rotate the group invite link and return the new one in `{\"invite\": \"...\"}`.",
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
		Description: "Join the group from an invite code or link and return `groupJid`.",
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
		Description: "Remove the session's own account from the group.\n\n" +
			"Returns 204 on success.",
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
		Description:   "Not implemented in v2. Requires `send`, then returns `not_implemented`.",
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
