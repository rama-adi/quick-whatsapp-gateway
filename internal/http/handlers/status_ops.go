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
	Session string `path:"session" doc:"The WhatsApp session id — one attached WhatsApp number. The status is posted from this account's \"My Status\" story. Must be a session owned by the caller's organization; an unknown or cross-org id yields not_found (404)." example:"sess_01HZX9K3J7"`
	Body    struct {
		Type string `json:"type,omitempty" doc:"The kind of status to post. Optional on the wire (an empty value is treated as \"text\"), but the service still validates it.\n\n- **text** — a text-only \"My Status\" story. The only kind supported in v1.\n- **image** — an image status. Returns not_implemented (501) in v1; reserved for a later release.\n\nAny value other than these two also returns not_implemented (501)." enum:"text,image" example:"text"`
		Text string `json:"text,omitempty" doc:"The status text. Required (non-empty) when type is \"text\" or omitted; an empty or whitespace-only value yields validation_error (400). Ignored for non-text types." example:"Out of office until Monday."`
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
	Session string `path:"session" doc:"The WhatsApp session id — one attached WhatsApp number whose account-wide availability is being set. Must be a session owned by the caller's organization; an unknown or cross-org id yields not_found (404)." example:"sess_01HZX9K3J7"`
	Body    struct {
		State string `json:"state,omitempty" doc:"The account-wide presence to set, controlling whether the account appears available to its contacts. Optional on the wire, but the service validates it: any value other than the two below yields validation_error (400).\n\n- **online** — the account is shown as available/active.\n- **offline** — the account is shown as unavailable.\n\nThis is account-wide. To send a per-chat \"typing…\" or \"recording…\" indicator into a single conversation, use the chat presence endpoint instead." enum:"online,offline" example:"online"`
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
		Summary: "Post a status update (text; media returns 501)",
		Description: "Post a status update — the \"My Status\" story — from this session's WhatsApp account.\n\n" +
			"**When to use.** Publish a short text update visible to the account's contacts (subject to WhatsApp's own status-privacy settings).\n\n" +
			"**Capability.** Requires the `send` capability on the caller's identity.\n\n" +
			"**Preconditions.** The `{session}` must belong to the caller's organization and be paired/connected; an unknown or cross-org session yields `not_found` (404).\n\n" +
			"**Supported types.** Only `type: text` works in v1. The `type` field may be omitted, in which case it defaults to `text`.\n\n" +
			"**Side effects.** On success the status story is published on WhatsApp and a message id is returned. There is no built-in idempotency key for this endpoint — re-posting the same text publishes a new status each time, so callers that retry must dedupe themselves.\n\n" +
			"**Result.** Returns `200` with the published WhatsApp message id (`messageId`).\n\n" +
			"**Errors.**\n" +
			"- `validation_error` (400) — empty/whitespace `text` for a text status.\n" +
			"- `not_found` (404) — the session does not exist or is not owned by the caller's organization.\n" +
			"- `forbidden` (403) — the caller lacks the `send` capability.\n" +
			"- `not_implemented` (501) — `type: image` or any non-text type; media statuses are not implemented in v1 (consistent with media send).",
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
		Summary: "Set account presence (online / offline)",
		Description: "Set the session account's **global** presence to `online` or `offline` — whether the account's contacts see it as available.\n\n" +
			"**When to use.** Mark the account available (`online`) or away (`offline`) account-wide. This is *not* per-chat: to send a \"typing…\" or \"recording…\" indicator into one conversation, use the chat presence endpoint (`PUT /sessions/{session}/chats/{cid}/presence`) instead.\n\n" +
			"**Capability.** Requires the `send` capability on the caller's identity.\n\n" +
			"**Preconditions.** The `{session}` must belong to the caller's organization and be paired/connected; an unknown or cross-org session yields `not_found` (404).\n\n" +
			"**Idempotency.** Setting the presence to a value it already holds is a no-op as far as observers are concerned; the call is safe to repeat.\n\n" +
			"**Result.** Returns `204 No Content` on success (empty body).\n\n" +
			"**Errors.**\n" +
			"- `validation_error` (400) — `state` is missing or not one of `online` / `offline`.\n" +
			"- `not_found` (404) — the session does not exist or is not owned by the caller's organization.\n" +
			"- `forbidden` (403) — the caller lacks the `send` capability.\n" +
			"- `not_implemented` (501) — a presence mode not supported by the engine.",
		Tags:          []string{"Presence"},
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
