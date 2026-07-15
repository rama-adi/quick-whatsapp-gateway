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

// maxSendMessageBody allows the 16 MiB decoded media limit to be represented as
// base64 JSON (roughly 21.4 MiB) with room for captions and other metadata.
// Other Huma operations retain the framework's 1 MiB default request limit.
// Albums allow up to 64 MiB decoded across ten inline items; base64 expansion
// plus JSON metadata fits within this request ceiling.
const maxSendMessageBody int64 = 88 << 20

// sendMessageInput is POST /sessions/{session}/messages.
type sendMessageInput struct {
	Session        string `path:"session" doc:"WhatsApp session id that sends the message. Must be owned and connected." example:"01HZX..."`
	Async          bool   `query:"async" doc:"Set true to queue the send and return 202. False (default) waits for WhatsApp and returns 200." example:"false"`
	IdempotencyKey string `header:"Idempotency-Key" doc:"Optional idempotency token. Reusing the key returns the first send result and does not send again." example:"2f1c9b6e-7a3d-4c2e-9f8a-1b2c3d4e5f60"`
	Body           domain.SendRequest
}

// sendMessageOutput carries the SendResult. Status is 200 or 202.
type sendMessageOutput struct {
	Status int
	Body   outbound.SendResult
}

// editMessageInput is PATCH /sessions/{session}/messages/{mid}.
type editMessageInput struct {
	Session string `path:"session" doc:"WhatsApp session id that sent the message. Must be owned and connected." example:"01HZX..."`
	MID     string `path:"mid" doc:"WhatsApp message id of the text message to edit." example:"3EB0C431C26A1916E07A"`
	Body    struct {
		Chat string `json:"chat,omitempty" doc:"Chat JID for the message." example:"6281234567890@s.whatsapp.net"`
		Text string `json:"text,omitempty" doc:"New text content. Only text messages are supported." example:"Updated message text"`
	}
}

// revokeMessageInput is DELETE /sessions/{session}/messages/{mid}.
type revokeMessageInput struct {
	Session string `path:"session" doc:"WhatsApp session id that issues the revoke. Must be owned and connected." example:"01HZX..."`
	MID     string `path:"mid" doc:"WhatsApp message id to delete for everyone." example:"3EB0C431C26A1916E07A"`
	Body    struct {
		Chat   string `json:"chat,omitempty" doc:"Chat JID the message belongs to." example:"6281234567890@s.whatsapp.net"`
		Sender string `json:"sender,omitempty" doc:"Original sender JID. Leave empty for your own message." example:""`
	}
}

// reactionInput is POST/DELETE /sessions/{session}/messages/{mid}/reaction.
type reactionInput struct {
	Session string `path:"session" doc:"WhatsApp session id. Must be owned and connected." example:"01HZX..."`
	MID     string `path:"mid" doc:"WhatsApp message id to react to." example:"3EB0C431C26A1916E07A"`
	Body    struct {
		Chat   string `json:"chat,omitempty" doc:"Chat JID for the message." example:"6281234567890@s.whatsapp.net"`
		Sender string `json:"sender,omitempty" doc:"Original sender JID. Empty means your own message." example:""`
		Emoji  string `json:"emoji,omitempty" doc:"Single emoji. Required for add; ignored for remove." example:"👍"`
	}
}

// forwardInput is POST /sessions/{session}/messages/{mid}/forward.
type forwardInput struct {
	Session string `path:"session" doc:"WhatsApp session id that forwards the message. Must be owned and connected." example:"01HZX..."`
	MID     string `path:"mid" doc:"WhatsApp message id to forward." example:"3EB0C431C26A1916E07A"`
	Body    struct {
		Chat   string `json:"chat,omitempty" doc:"Source chat JID." example:"6281234567890@s.whatsapp.net"`
		Sender string `json:"sender,omitempty" doc:"Original sender JID. Empty means your own message." example:""`
		To     string `json:"to,omitempty" doc:"Destination chat JID." example:"6289876543210@s.whatsapp.net"`
	}
}

// voteInput is POST /sessions/{session}/messages/{mid}/vote.
type voteInput struct {
	Session string `path:"session" doc:"WhatsApp session id casting the vote. Must be owned and connected." example:"01HZX..."`
	MID     string `path:"mid" doc:"WhatsApp message id of the poll." example:"3EB0C431C26A1916E07A"`
	Body    struct {
		Chat    string   `json:"chat,omitempty" doc:"Chat JID of the poll." example:"120363012345678901@g.us"`
		Sender  string   `json:"sender,omitempty" doc:"Poll sender JID." example:""`
		Options []string `json:"options,omitempty" doc:"Complete list of selected options. Send empty list to clear vote." example:"[\"Pizza\",\"Sushi\"]"`
	}
}

// sendResultOutput is the success body for message operations.
type sendResultOutput struct{ Body outbound.SendResult }

// RegisterMessageOps registers outbound message operations.
func RegisterMessageOps(api huma.API, h *Handlers) {
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "sendMessage", Method: "POST", Path: "/api/v1/sessions/{session}/messages",
		MaxBodyBytes:    maxSendMessageBody,
		BodyReadTimeout: -1,
		Summary:         "Send a message",
		Description: "Send one message or one grouped media album from the session.\n\n" +
			"Use `type` in the body to select a supported payload (`text`, `poll`, `location`, `contact`).\n\n" +
			"Default mode is synchronous and returns 200. Set `async=true` for queued async sends that return 202.\n" +
			"Idempotency is enabled with `Idempotency-Key`.\n\n" +
			"Errors: `validation_error`, `not_found`, `rate_limited`, and `not_implemented` for unsupported types.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *sendMessageInput) (*sendMessageOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		opts := outbound.SendOptions{Async: in.Async, IdempotencyKey: in.IdempotencyKey}
		res, err := h.Messages.Send(ctx, org, in.Session, in.Body, opts)
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		status := http.StatusOK
		if res.Mode == outbound.ModeAsync {
			status = http.StatusAccepted
		}
		return &sendMessageOutput{Status: status, Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "editMessage", Method: "PATCH", Path: "/api/v1/sessions/{session}/messages/{mid}",
		Summary: "Edit a sent text message",
		Description: "Replace the text content of a message sent by this session.\n\n" +
			"Only text messages are editable. Returns 200 with a SendResult.\n\n" +
			"Errors: `validation_error` for missing/invalid text and `not_found` for missing message or session.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *editMessageInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Edit(ctx, org, in.Session, in.Body.Chat, in.MID, in.Body.Text)
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revokeMessage", Method: "DELETE", Path: "/api/v1/sessions/{session}/messages/{mid}",
		Summary: "Revoke a message (delete for everyone)",
		Description: "Delete a message for everyone in the chat.\n\n" +
			"Use `sender` when targeting another participant's message. Returns 200 with a SendResult.\n\n" +
			"Errors: `not_found` for missing message or session.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *revokeMessageInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Revoke(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID)
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "addReaction", Method: "POST", Path: "/api/v1/sessions/{session}/messages/{mid}/reaction",
		Summary: "Add a reaction to a message",
		Description: "Set your reaction emoji for a message.\n\n" +
			"Returns 200 with a SendResult.\n\n" +
			"Errors: `validation_error` when emoji is missing and `not_found` for missing message or session.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *reactionInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.React(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, in.Body.Emoji)
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "removeReaction", Method: "DELETE", Path: "/api/v1/sessions/{session}/messages/{mid}/reaction",
		Summary: "Remove your reaction from a message",
		Description: "Clear your reaction from a message.\n\n" +
			"The operation is idempotent.\n\n" +
			"Errors: `not_found` for missing message or session.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *reactionInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		// Empty emoji clears the reaction.
		res, err := h.Messages.React(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, "")
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "forwardMessage", Method: "POST", Path: "/api/v1/sessions/{session}/messages/{mid}/forward",
		Summary: "Forward a message to another chat",
		Description: "Forward a message to a destination chat.\n\n" +
			"Returns 200 with a SendResult for the new message.\n\n" +
			"Errors: `validation_error` when `to` is missing and `not_found` for missing message or session.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *forwardInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Forward(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, in.Body.To)
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "voteMessage", Method: "POST", Path: "/api/v1/sessions/{session}/messages/{mid}/vote",
		Summary: "Vote on a poll message",
		Description: "Replace your poll vote.\n\n" +
			"Use `options` for the full set of selected choices. Send empty list to clear vote.\n\n" +
			"Returns 200 with a SendResult.\n\n" +
			"Errors: `validation_error` for bad poll/options and `not_found` for missing message or session.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *voteInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Vote(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, in.Body.Options)
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &sendResultOutput{Body: res}, nil
	})
}
