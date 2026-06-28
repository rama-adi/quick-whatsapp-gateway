package handlers

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// sendMessageInput is POST /sessions/{session}/messages. The body is the
// discriminated domain.SendRequest; Idempotency-Key (header) and ?async (query)
// tune delivery, mirroring the chi handler. The service validates the body, so it
// is passed through untouched.
type sendMessageInput struct {
	Session        string `path:"session"`
	Async          bool   `query:"async"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           domain.SendRequest
}

// sendMessageOutput carries the SendResult. Status is set at runtime to 200
// (sync) or 202 (async), matching the chi handler's status split.
type sendMessageOutput struct {
	Status int
	Body   outbound.SendResult
}

// editMessageInput is PATCH /sessions/{session}/messages/{mid}. Body fields are
// optional on the wire; the service validates.
type editMessageInput struct {
	Session string `path:"session"`
	MID     string `path:"mid"`
	Body    struct {
		Chat string `json:"chat,omitempty"`
		Text string `json:"text,omitempty"`
	}
}

// revokeMessageInput is DELETE /sessions/{session}/messages/{mid}. sender is the
// original sender JID ("" for your own message).
type revokeMessageInput struct {
	Session string `path:"session"`
	MID     string `path:"mid"`
	Body    struct {
		Chat   string `json:"chat,omitempty"`
		Sender string `json:"sender,omitempty"`
	}
}

// reactionInput is POST/DELETE /sessions/{session}/messages/{mid}/reaction.
type reactionInput struct {
	Session string `path:"session"`
	MID     string `path:"mid"`
	Body    struct {
		Chat   string `json:"chat,omitempty"`
		Sender string `json:"sender,omitempty"`
		Emoji  string `json:"emoji,omitempty"`
	}
}

// forwardInput is POST /sessions/{session}/messages/{mid}/forward.
type forwardInput struct {
	Session string `path:"session"`
	MID     string `path:"mid"`
	Body    struct {
		Chat   string `json:"chat,omitempty"`
		Sender string `json:"sender,omitempty"`
		To     string `json:"to,omitempty"`
	}
}

// voteInput is POST /sessions/{session}/messages/{mid}/vote.
type voteInput struct {
	Session string `path:"session"`
	MID     string `path:"mid"`
	Body    struct {
		Chat    string   `json:"chat,omitempty"`
		Sender  string   `json:"sender,omitempty"`
		Options []string `json:"options,omitempty"`
	}
}

// sendResultOutput is the success body for the message-op endpoints (edit,
// revoke, react, forward, vote) — all 200 with a SendResult body.
type sendResultOutput struct{ Body outbound.SendResult }

// RegisterMessageOps registers the outbound send + message-op operations (send
// capability) on the huma API. Code-first replacement for the chi messages group.
func RegisterMessageOps(api huma.API, h *Handlers) {
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "sendMessage", Method: "POST", Path: "/api/v1/sessions/{session}/messages",
		Summary: "Send a message", Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *sendMessageInput) (*sendMessageOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		opts := outbound.SendOptions{Async: in.Async, IdempotencyKey: in.IdempotencyKey}
		res, err := h.Messages.Send(ctx, org, in.Session, in.Body, opts)
		if err != nil {
			return nil, humax.Err(err)
		}
		status := http.StatusOK
		if res.Mode == outbound.ModeAsync {
			status = http.StatusAccepted
		}
		return &sendMessageOutput{Status: status, Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "editMessage", Method: "PATCH", Path: "/api/v1/sessions/{session}/messages/{mid}",
		Summary: "Edit a message", Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *editMessageInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Edit(ctx, org, in.Session, in.Body.Chat, in.MID, in.Body.Text)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revokeMessage", Method: "DELETE", Path: "/api/v1/sessions/{session}/messages/{mid}",
		Summary: "Revoke (delete) a message", Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *revokeMessageInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Revoke(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "addReaction", Method: "POST", Path: "/api/v1/sessions/{session}/messages/{mid}/reaction",
		Summary: "Add a reaction", Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *reactionInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.React(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, in.Body.Emoji)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "removeReaction", Method: "DELETE", Path: "/api/v1/sessions/{session}/messages/{mid}/reaction",
		Summary: "Remove a reaction", Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *reactionInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		// An empty emoji removes the reaction, matching the chi handler.
		res, err := h.Messages.React(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, "")
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "forwardMessage", Method: "POST", Path: "/api/v1/sessions/{session}/messages/{mid}/forward",
		Summary: "Forward a message", Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *forwardInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Forward(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, in.Body.To)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "voteMessage", Method: "POST", Path: "/api/v1/sessions/{session}/messages/{mid}/vote",
		Summary: "Vote in a poll", Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *voteInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Vote(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, in.Body.Options)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})
}
