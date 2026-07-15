package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

// postStatusInput is POST /sessions/{session}/status. Body fields are optional on
// the wire so the service stays the validator (domain.ErrValidation → 400). Only
// type "text" is supported in v1; any other type returns not_implemented (501),
// matching the chi handler.
type postStatusInput struct {
	Session string `path:"session" doc:"Session ID that owns this status. Must belong to your organization." example:"sess_01HZX9K3J7"`
	Body    struct {
		Type string `json:"type,omitempty" doc:"Status type. Supported in v1: text (default if omitted). image or unknown values return not_implemented (501)." enum:"text,image" example:"text"`
		Text string `json:"text,omitempty" doc:"Status text. Required for text status. Empty values return validation_error (400)." example:"Out of office until Monday."`
	}
}

type postStatusOutput struct {
	Body struct {
		MessageID string `json:"messageId"`
	}
}

// setPresenceInput is PUT /sessions/{session}/presence. The state field is
// optional on the wire so the service validates it (domain.ErrValidation → 400).
type setPresenceInput struct {
	Session string `path:"session" doc:"Session ID for which to set account presence. Must belong to your organization." example:"sess_01HZX9K3J7"`
	Body    struct {
		State string `json:"state,omitempty" doc:"Presence mode: online or offline. Any other value returns validation_error (400). This is account-wide; use chat presence endpoint for typing/recording indicators." enum:"online,offline" example:"online"`
	}
}

// RegisterStatusOps registers the status and presence operations (send
// capability) on the huma API. Code-first replacement for the status/presence
// routes in the chi resources group. The 501 not_implemented behavior for
// non-text status is preserved.
func RegisterStatusOps(api huma.API, h *Handlers) {
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "postStatus", Method: "POST", Path: "/api/v1/sessions/{session}/status",
		Summary: "Post a status update",
		Description: "Publish a text status story from this session.\n\n" +
			"Only `type: text` is implemented. Omit `type` to default to text; other values return `not_implemented` (501).\n\n" +
			"Requires `send` capability. Session must belong to your organization.\n\n" +
			"Returns `messageId` on success.\n\n" +
			"Errors: `validation_error` (400), `not_found` (404), `forbidden` (403), `not_implemented` (501).",
		Tags: []string{"Status"}, Middlewares: send,
	}, func(ctx context.Context, in *postStatusInput) (*postStatusOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		switch in.Body.Type {
		case "", "text":
			id, err := h.Status.PostText(ctx, org, in.Session, in.Body.Text)
			if err != nil {
				return nil, humax.ErrContext(ctx, err)
			}
			out := &postStatusOutput{}
			out.Body.MessageID = id
			return out, nil
		default:
			// image / video / any media status is 501 in v1, consistent with media send.
			return nil, humax.ErrContext(ctx, domain.ErrNotImplemented(in.Body.Type+" status is not implemented yet"))
		}
	})

	huma.Register(api, huma.Operation{
		OperationID: "setPresence", Method: "PUT", Path: "/api/v1/sessions/{session}/presence",
		Summary: "Set account presence",
		Description: "Set account-wide presence for this session (`online` or `offline`).\n\n" +
			"Use chat presence endpoint for per-conversation typing/recording indicators.\n\n" +
			"Requires `send` capability. Session must belong to your organization.\n\n" +
			"Returns `204 No Content` on success.\n\n" +
			"Errors: `validation_error` (400), `not_found` (404), `forbidden` (403), `not_implemented` (501).",
		Tags:          []string{"Presence"},
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *setPresenceInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Presence.Set(ctx, org, in.Session, in.Body.State); err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &emptyOutput{}, nil
	})
}
