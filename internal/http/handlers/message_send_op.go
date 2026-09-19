package handlers

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/humax"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

// RegisterSendMessageOp registers POST /sessions/{session}/messages on any
// huma API. Since gRPC Increment 6 the send pipeline is API-owned: mounting
// this op on the public API router serves sends from the OutboundScheduler
// through the private engine, while the gateway keeps it for control-disabled
// deployments until Increment 9 removes the legacy HTTP surface.
func RegisterSendMessageOp(api huma.API, h *Handlers) {
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
		status := http.StatusOK
		if res.Mode == outbound.ModeAsync {
			status = http.StatusAccepted
		}
		return &sendMessageOutput{Status: status, Body: res}, nil
	})
}
