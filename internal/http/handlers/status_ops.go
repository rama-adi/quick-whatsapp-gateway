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
	Session string `path:"session"`
	Body    struct {
		Type string `json:"type,omitempty"`
		Text string `json:"text,omitempty"`
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
	Session string `path:"session"`
	Body    struct {
		State string `json:"state,omitempty"`
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
		Summary: "Post a status update", Tags: []string{"Status"}, Middlewares: send,
	}, func(ctx context.Context, in *postStatusInput) (*postStatusOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		switch in.Body.Type {
		case "", "text":
			id, err := h.Status.PostText(ctx, org, in.Session, in.Body.Text)
			if err != nil {
				return nil, humax.Err(err)
			}
			out := &postStatusOutput{}
			out.Body.MessageID = id
			return out, nil
		default:
			// image / video / any media status is 501 in v1, consistent with media send.
			return nil, humax.Err(domain.ErrNotImplemented(in.Body.Type + " status is not implemented yet"))
		}
	})

	huma.Register(api, huma.Operation{
		OperationID: "setPresence", Method: "PUT", Path: "/api/v1/sessions/{session}/presence",
		Summary: "Set account presence", Tags: []string{"Presence"},
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *setPresenceInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Presence.Set(ctx, org, in.Session, in.Body.State); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})
}
