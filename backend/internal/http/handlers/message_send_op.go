package handlers

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/humax"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

func pendingSendResponses() map[string]*huma.Response {
	return map[string]*huma.Response{
		"202": {
			Description: "Accepted; the durable command is queued or sending",
			Content: map[string]*huma.MediaType{
				"application/json": {Schema: &huma.Schema{Ref: "#/components/schemas/SendResult"}},
			},
		},
		"default": {
			Description: "Error",
			Content: map[string]*huma.MediaType{
				"application/json": {Schema: &huma.Schema{Ref: "#/components/schemas/ApiError"}},
			},
		},
	}
}

func sendResultStatus(result outbound.SendResult) int {
	if result.Mode == outbound.ModeAsync {
		return http.StatusAccepted
	}
	return http.StatusOK
}

// RegisterSendMessageOp registers POST /sessions/{session}/messages on any
// huma API. Since gRPC Increment 6 the send pipeline is API-owned: mounting
// this op on the public API router serves sends from the OutboundScheduler
// through the private engine, while the gateway keeps it for control-disabled
// deployments until Increment 9 removes the legacy HTTP surface.
func RegisterSendMessageOp(api huma.API, h *Handlers) {
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "getOutboxByIdempotency", Method: http.MethodGet,
		Path:        "/api/v1/sessions/{session}/outbox/by-idempotency",
		Summary:     "Look up an outbound command",
		Description: "Read the current durable result for a previously accepted idempotency key in this organization and session. Requires send capability. Returns 200 with mode=sync for sent, failed, or unknown; 202 with mode=async for queued or sending. A missing or foreign key returns 404. The lookup has no delivery side effects.",
		Tags:        []string{"Messages"}, Middlewares: send,
		Responses: pendingSendResponses(),
	}, func(ctx context.Context, in *struct {
		Session string `path:"session" doc:"Session that owns the outbound command."`
		Key     string `query:"key" required:"true" doc:"Previously accepted Idempotency-Key value."`
	}) (*sendMessageOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		result, err := h.Messages.LookupByIdempotency(ctx, org, in.Session, in.Key)
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &sendMessageOutput{Status: sendResultStatus(result), Body: result}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sendMessage", Method: "POST", Path: "/api/v1/sessions/{session}/messages",
		MaxBodyBytes:    maxSendMessageBody,
		BodyReadTimeout: -1,
		Summary:         "Send a message",
		Description: "Send one message or one grouped media album from the session.\n\n" +
			"Use `type` in the body to select a supported payload (`text`, `poll`, `location`, `contact`, `buttons`, `list`).\n\n" +
			"Buttons use native-flow quick replies; lists use legacy ListMessage. Both are experimental, including for group JIDs. A send acknowledgement does not guarantee client rendering. WhatsApp may reject a list with 405; this returns not_implemented and is not retried.\n\n" +
			"Default mode is synchronous and returns 200 after a validated send. Set `async=true` for queued sends that return 202. A terminal replay also returns 200, including status=unknown when a remote effect may have committed.\n" +
			"Idempotency is enabled with `Idempotency-Key`; replay returns the current durable result in the original session. Optional expiresAt is an absolute UTC epoch millisecond frozen by the first accepted row. No new WhatsApp dispatch starts at or after that deadline.\n\n" +
			"Errors: `validation_error`, `not_found`, `rate_limited`, and `not_implemented` for unsupported types.",
		Tags: []string{"Messages"}, Middlewares: send,
		Responses: pendingSendResponses(),
	}, func(ctx context.Context, in *sendMessageInput) (*sendMessageOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		opts := outbound.SendOptions{Async: in.Async, IdempotencyKey: in.IdempotencyKey}
		res, err := h.Messages.Send(
			ctx,
			org,
			in.Session,
			in.Body,
			opts,
		)
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &sendMessageOutput{Status: sendResultStatus(res), Body: res}, nil
	})
}
