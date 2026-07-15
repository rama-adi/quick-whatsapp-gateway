package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

// createChannelInput is POST /sessions/{session}/channels. Body fields are
// optional on the wire so the service stays the validator (domain.ErrValidation
// → 400), matching the chi handler's behavior.
type createChannelInput struct {
	Session string `path:"session" doc:"Session ID that owns the channel. Must belong to your organization." example:"01HX..."`
	Body    struct {
		Name        string `json:"name,omitempty" doc:"Channel name. Optional in payload; empty or invalid names are rejected as validation_error (400)." example:"Acme Announcements"`
		Description string `json:"description,omitempty" doc:"Optional channel description." example:"Product updates and release notes."`
	}
}

// channelJIDInput is the no-payload action shape (:follow / :unfollow).
type channelJIDInput struct {
	Session string `path:"session" doc:"Session ID for the follow/unfollow/mute action." example:"01HX..."`
	JID     string `path:"jid" doc:"Channel JID, for example 120363012345678901@newsletter. URL encoding for reserved chars is handled by the gateway." example:"120363012345678901@newsletter"`
}

// muteChannelInput is POST /sessions/{session}/channels/{jid}:mute. The body is
// optional; an absent {"mute":...} defaults to muting (matching the chi handler).
type muteChannelInput struct {
	Session string `path:"session" doc:"Session ID whose channel mute flag is being updated." example:"01HX..."`
	JID     string `path:"jid" doc:"Channel JID, for example 120363012345678901@newsletter." example:"120363012345678901@newsletter"`
	Body    struct {
		Mute *bool `json:"mute,omitempty" doc:"Mute state: true mutes, false unmutes. Empty body defaults to mute=true." example:"true"`
	}
}

// listChannelMessagesInput is GET /sessions/{session}/channels/{jid}/messages.
type listChannelMessagesInput struct {
	Session string `path:"session" doc:"Session ID whose stored channel messages are being queried." example:"01HX..."`
	JID     string `path:"jid" doc:"Channel JID, for example 120363012345678901@newsletter." example:"120363012345678901@newsletter"`
	Limit   int    `query:"limit" doc:"Max messages to return. Omit or set 0 to use default 50. Accepted range is 1-200." example:"50"`
	Cursor  string `query:"cursor" doc:"Opaque cursor for pagination. Omit to fetch first page; pass response nextCursor to fetch next." example:""`
}

type createChannelOutput struct {
	Body struct {
		JID string `json:"jid"`
	}
}
type channelMessageListOutput struct{ Body apitypes.List[domain.Message] }

// RegisterChannelOps registers the channel/newsletter operations (send
// capability) on the huma API. Code-first replacement for the channels routes in
// the chi resources group. The 501 not_implemented behavior is preserved: the
// service may return domain.ErrNotImplemented and humax.Err maps it to 501.
func RegisterChannelOps(api huma.API, h *Handlers) {
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "createChannel", Method: "POST", Path: "/api/v1/sessions/{session}/channels",
		Summary: "Create a channel (newsletter)", Tags: []string{"Channels"},
		Description: "Create a WhatsApp channel owned by this session and return its JID.\n\n" +
			"Current behavior is `not_implemented` (501): request shape is validated but channel is not created yet.\n\n" +
			"Requires `send` capability.\n\n" +
			"Errors: `forbidden` (403), `not_found` (404), `not_implemented` (501), `validation_error` (400).",
		DefaultStatus: 201, Middlewares: send,
	}, func(ctx context.Context, in *createChannelInput) (*createChannelOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		jid, err := h.Channels.Create(ctx, org, in.Session, in.Body.Name, in.Body.Description)
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		out := &createChannelOutput{}
		out.Body.JID = jid
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "followChannel", Method: "POST", Path: "/api/v1/sessions/{session}/channels/{jid}:follow",
		Summary: "Follow a channel", Tags: []string{"Channels"},
		Description: "Subscribe the session to a channel.\n\n" +
			"Current behavior is `not_implemented` (501).\n\n" +
			"Requires `send` capability.\n\n" +
			"No request body; action returns 204 when supported.\n\n" +
			"Errors: `forbidden` (403), `not_found` (404), `not_implemented` (501).",
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *channelJIDInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Channels.Follow(ctx, org, in.Session, decodeParam(in.JID)); err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "unfollowChannel", Method: "POST", Path: "/api/v1/sessions/{session}/channels/{jid}:unfollow",
		Summary: "Unfollow a channel", Tags: []string{"Channels"},
		Description: "Stop following the channel for this session.\n\n" +
			"Current behavior is `not_implemented` (501).\n\n" +
			"Requires `send` capability.\n\n" +
			"No request body; returns 204 when supported.\n\n" +
			"Errors: `forbidden` (403), `not_found` (404), `not_implemented` (501).",
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *channelJIDInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Channels.Unfollow(ctx, org, in.Session, decodeParam(in.JID)); err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "muteChannel", Method: "POST", Path: "/api/v1/sessions/{session}/channels/{jid}:mute",
		Summary: "Mute or unmute a channel", Tags: []string{"Channels"},
		Description: "Set channel mute state for this session. Omit body or `mute` for default mute=true.\n\n" +
			"Current behavior is `not_implemented` (501); no state change happens yet.\n\n" +
			"Requires `send` capability.\n\n" +
			"Returns 204 when supported.\n\n" +
			"Errors: `forbidden` (403), `not_found` (404), `not_implemented` (501).",
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *muteChannelInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		mute := true // default to muting; pass {"mute":false} to unmute
		if in.Body.Mute != nil {
			mute = *in.Body.Mute
		}
		if err := h.Channels.Mute(ctx, org, in.Session, decodeParam(in.JID), mute); err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "listChannelMessages", Method: "GET", Path: "/api/v1/sessions/{session}/channels/{jid}/messages",
		Summary: "List stored channel messages", Tags: []string{"Channels"}, Middlewares: send,
		Description: "List messages already stored on the gateway for this channel, newest first.\n\n" +
			"Pagination uses `limit` (default 50, 1-200) and `cursor` (`nextCursor` from response).\n\n" +
			"Safe, side-effect-free read.\n\n" +
			"Requires `send` capability.\n\n" +
			"Errors: `forbidden` (403), `not_found` (404).",
	}, func(ctx context.Context, in *listChannelMessagesInput) (*channelMessageListOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		page, err := h.Channels.Messages(ctx, org, in.Session, decodeParam(in.JID), in.Cursor, clampLimit(in.Limit))
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &channelMessageListOutput{Body: apitypes.NewList(page.Items, page.NextCursor)}, nil
	})
}
