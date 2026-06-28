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
	Session string `path:"session"`
	Body    struct {
		Name         string   `json:"name,omitempty"`
		Participants []string `json:"participants,omitempty"`
	}
}

// listGroupsInput is GET /sessions/{session}/groups.
type listGroupsInput struct {
	Session string `path:"session"`
}

// getGroupInput is GET /sessions/{session}/groups/{gid}.
type getGroupInput struct {
	Session string `path:"session"`
	GID     string `path:"gid"`
}

// listGroupMembersInput is GET /sessions/{session}/groups/{gid}/members.
type listGroupMembersInput struct {
	Session string `path:"session"`
	GID     string `path:"gid"`
}

// addGroupMembersInput is POST /sessions/{session}/groups/{gid}/members.
type addGroupMembersInput struct {
	Session string `path:"session"`
	GID     string `path:"gid"`
	Body    struct {
		Participants []string `json:"participants,omitempty"`
	}
}

// removeGroupMemberInput is DELETE /sessions/{session}/groups/{gid}/members/{jid}.
type removeGroupMemberInput struct {
	Session string `path:"session"`
	GID     string `path:"gid"`
	JID     string `path:"jid"`
}

// promoteGroupMemberInput is POST /sessions/{session}/groups/{gid}/members/{jid}/promote.
type promoteGroupMemberInput struct {
	Session string `path:"session"`
	GID     string `path:"gid"`
	JID     string `path:"jid"`
}

// demoteGroupMemberInput is POST /sessions/{session}/groups/{gid}/members/{jid}/demote.
type demoteGroupMemberInput struct {
	Session string `path:"session"`
	GID     string `path:"gid"`
	JID     string `path:"jid"`
}

// updateGroupInput is PATCH /sessions/{session}/groups/{gid}.
type updateGroupInput struct {
	Session string `path:"session"`
	GID     string `path:"gid"`
	Body    struct {
		Subject     *string `json:"subject,omitempty"`
		Description *string `json:"description,omitempty"`
		Announce    *bool   `json:"announce,omitempty"`
		Locked      *bool   `json:"locked,omitempty"`
	}
}

// getGroupInviteInput is GET /sessions/{session}/groups/{gid}/invite.
type getGroupInviteInput struct {
	Session string `path:"session"`
	GID     string `path:"gid"`
}

// revokeGroupInviteInput is DELETE /sessions/{session}/groups/{gid}/invite.
type revokeGroupInviteInput struct {
	Session string `path:"session"`
	GID     string `path:"gid"`
}

// joinGroupInput is POST /sessions/{session}/groups:join.
type joinGroupInput struct {
	Session string `path:"session"`
	Body    struct {
		Invite string `json:"invite,omitempty"`
	}
}

// leaveGroupInput is POST /sessions/{session}/groups/{gid}:leave.
type leaveGroupInput struct {
	Session string `path:"session"`
	GID     string `path:"gid"`
}

// approveGroupMembersInput is POST /sessions/{session}/groups/{gid}/members:approve.
type approveGroupMembersInput struct {
	Session string `path:"session"`
	GID     string `path:"gid"`
	Body    struct {
		Participants []string `json:"participants,omitempty"`
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
		Summary: "List groups", Tags: []string{"Groups"}, Middlewares: read,
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
		Summary: "Get a group", Tags: []string{"Groups"}, Middlewares: read,
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
		Summary: "List group members", Tags: []string{"Groups"}, Middlewares: read,
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
		Summary: "Add group members", Tags: []string{"Groups"},
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
		Summary: "Remove a group member", Tags: []string{"Groups"},
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
		Summary: "Promote a group member to admin", Tags: []string{"Groups"},
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
		Summary: "Demote a group admin", Tags: []string{"Groups"},
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
		Summary: "Get the group invite link", Tags: []string{"Groups"}, Middlewares: read,
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
		Summary: "Revoke and reissue the group invite link", Tags: []string{"Groups"}, Middlewares: send,
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
		Summary: "Join a group by invite link", Tags: []string{"Groups"}, Middlewares: send,
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
		Summary: "Approve pending group join requests", Tags: []string{"Groups"},
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
