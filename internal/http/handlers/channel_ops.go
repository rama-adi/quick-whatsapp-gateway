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
	Session string `path:"session"`
	Body    struct {
		Name        string `json:"name,omitempty"`
		Description string `json:"description,omitempty"`
	}
}

// channelJIDInput is the no-payload action shape (:follow / :unfollow).
type channelJIDInput struct {
	Session string `path:"session"`
	JID     string `path:"jid"`
}

// muteChannelInput is POST /sessions/{session}/channels/{jid}:mute. The body is
// optional; an absent {"mute":...} defaults to muting (matching the chi handler).
type muteChannelInput struct {
	Session string `path:"session"`
	JID     string `path:"jid"`
	Body    struct {
		Mute *bool `json:"mute,omitempty"`
	}
}

// listChannelMessagesInput is GET /sessions/{session}/channels/{jid}/messages.
type listChannelMessagesInput struct {
	Session string `path:"session"`
	JID     string `path:"jid"`
	Limit   int    `query:"limit"`
	Cursor  string `query:"cursor"`
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
		Summary: "Create a channel", Tags: []string{"Channels"},
		DefaultStatus: 201, Middlewares: send,
	}, func(ctx context.Context, in *createChannelInput) (*createChannelOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		jid, err := h.Channels.Create(ctx, org, in.Session, in.Body.Name, in.Body.Description)
		if err != nil {
			return nil, humax.Err(err)
		}
		out := &createChannelOutput{}
		out.Body.JID = jid
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "followChannel", Method: "POST", Path: "/api/v1/sessions/{session}/channels/{jid}:follow",
		Summary: "Follow a channel", Tags: []string{"Channels"},
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *channelJIDInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Channels.Follow(ctx, org, in.Session, decodeParam(in.JID)); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "unfollowChannel", Method: "POST", Path: "/api/v1/sessions/{session}/channels/{jid}:unfollow",
		Summary: "Unfollow a channel", Tags: []string{"Channels"},
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *channelJIDInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Channels.Unfollow(ctx, org, in.Session, decodeParam(in.JID)); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "muteChannel", Method: "POST", Path: "/api/v1/sessions/{session}/channels/{jid}:mute",
		Summary: "Mute or unmute a channel", Tags: []string{"Channels"},
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
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "listChannelMessages", Method: "GET", Path: "/api/v1/sessions/{session}/channels/{jid}/messages",
		Summary: "List channel messages", Tags: []string{"Channels"}, Middlewares: send,
	}, func(ctx context.Context, in *listChannelMessagesInput) (*channelMessageListOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		page, err := h.Channels.Messages(ctx, org, in.Session, decodeParam(in.JID), in.Cursor, clampLimit(in.Limit))
		if err != nil {
			return nil, humax.Err(err)
		}
		return &channelMessageListOutput{Body: apitypes.NewList(page.Items, page.NextCursor)}, nil
	})
}
