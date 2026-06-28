package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
)

// webhookBody is the POST/PATCH /webhooks request body. Secret is the plaintext
// HMAC secret; it is encrypted at rest and never returned. Fields are optional on
// the wire — the service validates (e.g. url required) so the §11 validation_error
// envelope keeps coming from one place.
type webhookBody struct {
	SessionID     *string             `json:"sessionId,omitempty" doc:"Scope this webhook to a single session: only events produced by that session are delivered. Omit or set to null to receive events from **all** of the caller organization's sessions. Must reference a session owned by the caller's organization." example:"01HHX5..."`
	URL           string              `json:"url,omitempty" doc:"The HTTPS endpoint that receives the delivery POSTs. Required on create (validated server-side; a missing or malformed value yields a 422 \"validation_error\"). The gateway POSTs the event envelope (the same JSON shape pushed over the realtime WebSocket) to this URL as the request body." example:"https://example.com/hooks/whatsapp"`
	Events        []string            `json:"events,omitempty" doc:"Event types to deliver to this webhook. Use [\"*\"] to receive every event type; an explicit list (e.g. [\"message.received\", \"session.connected\"]) delivers only those types; an empty list delivers nothing. See the event catalog for the full set of type names." example:"[\"*\"]"`
	Secret        *string             `json:"secret,omitempty" doc:"Plaintext HMAC signing secret. When set, the gateway signs each delivery: it sends the lowercase-hex HMAC-SHA512 of the exact raw request body in the X-Webhook-Hmac header (alongside X-Webhook-Hmac-Algorithm: sha512), so the receiver can recompute it with the same secret and confirm authenticity. Stored encrypted at rest and **never returned** in any response. On update: send a new value to rotate the secret; omit the field to leave the stored secret unchanged." example:"whsec_3f9a...redacted"`
	CustomHeaders map[string]string   `json:"customHeaders,omitempty" doc:"Extra HTTP headers attached to every delivery POST. Applied last, so they can override the gateway's default headers. Use for static auth tokens or routing hints the receiver requires." example:"{\"X-Tenant\":\"acme\"}"`
	RetryPolicy   *domain.RetryPolicy `json:"retryPolicy,omitempty" doc:"How failed deliveries (non-2xx response or connection error) are retried. Omit to use the gateway default: exponential backoff with a 2s base delay (2s, 4s, 8s, …) for up to 15 attempts, after which the delivery is given up (dead-lettered)."`
	Active        *bool               `json:"active,omitempty" doc:"Whether the webhook is enabled. An inactive (false) webhook is kept but receives no deliveries. Defaults to enabled when omitted on create." example:"true"`
}

func (b webhookBody) toInput() service.WebhookInput {
	return service.WebhookInput{
		SessionID:     b.SessionID,
		URL:           b.URL,
		Events:        b.Events,
		Secret:        b.Secret,
		CustomHeaders: b.CustomHeaders,
		RetryPolicy:   b.RetryPolicy,
		Active:        b.Active,
	}
}

type createWebhookInput struct{ Body webhookBody }
type updateWebhookInput struct {
	ID   string `path:"id" doc:"The webhook id to update. Must reference a webhook owned by the caller's organization, otherwise the request fails with a 404 \"not_found\"." example:"01HHX5WEBHOOK..."`
	Body webhookBody
}
type webhookIDInput struct {
	ID string `path:"id" doc:"The webhook id. Must reference a webhook owned by the caller's organization, otherwise the request fails with a 404 \"not_found\"." example:"01HHX5WEBHOOK..."`
}
type webhookOutput struct{ Body domain.Webhook }
type webhookListOutput struct{ Body apitypes.List[domain.Webhook] }
type emptyOutput struct{}

// RegisterWebhookOps registers the webhook CRUD operations (manage capability) on
// the huma API. This is the code-first replacement for the chi webhooks group.
func RegisterWebhookOps(api huma.API, h *Handlers) {
	manage := huma.Middlewares{humax.RequireCap(api, authz.CapManage)}

	huma.Register(api, huma.Operation{
		OperationID: "createWebhook", Method: "POST", Path: "/api/v1/webhooks",
		Summary: "Create a webhook",
		Description: "Register a webhook endpoint for the caller's organization. Requires the `manage` capability.\n\n" +
			"When a matching event fires, the gateway sends an HTTP `POST` to `url` whose body is the event envelope — the same JSON shape delivered over the realtime WebSocket. Use `events` to choose the event types to receive (`[\"*\"]` for all) and `sessionId` to scope deliveries to one session (null/omitted = all of the organization's sessions).\n\n" +
			"**Signing.** If you set a `secret`, every `POST` is signed: the gateway sends the lowercase-hex HMAC-SHA512 of the exact raw request body in the `X-Webhook-Hmac` header (with `X-Webhook-Hmac-Algorithm: sha512`). Recompute it on your end with the same secret to confirm the request really came from the gateway. The secret is stored encrypted and is never returned in any response.\n\n" +
			"**Delivery headers.** Every `POST` also carries `X-Webhook-Request-Id` (the event id — use it to drop duplicate redeliveries) and `X-Webhook-Timestamp` (epoch milliseconds). Any `customHeaders` are applied last and can override the defaults.\n\n" +
			"**Retries.** A delivery that returns non-2xx or fails to connect is retried on the `retryPolicy` schedule — by default exponential backoff (2s, 4s, 8s, …) for up to 15 attempts, after which it is given up (dead-lettered).\n\n" +
			"On success returns `201` with the created webhook. Errors: `422 validation_error` if the body is malformed (e.g. `url` missing or not a valid URL); `403 forbidden` if the caller lacks the `manage` capability.",
		Tags:          []string{"Webhooks"},
		DefaultStatus: 201, Middlewares: manage,
	}, func(ctx context.Context, in *createWebhookInput) (*webhookOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		hook, err := h.Webhooks.Create(ctx, org, in.Body.toInput())
		if err != nil {
			return nil, humax.Err(err)
		}
		return &webhookOutput{Body: hook}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "listWebhooks", Method: "GET", Path: "/api/v1/webhooks",
		Summary: "List webhooks",
		Description: "List the webhook endpoints configured for the caller's organization. Requires the `manage` capability.\n\n" +
			"The response is scoped to the caller's organization — webhooks owned by other organizations are never returned, and secrets are never included.\n\n" +
			"This endpoint returns the full set in one page (the response carries an empty `nextCursor`); no pagination parameters are accepted. Errors: `403 forbidden` if the caller lacks the `manage` capability.",
		Tags: []string{"Webhooks"}, Middlewares: manage,
	}, func(ctx context.Context, _ *struct{}) (*webhookListOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		hooks, err := h.Webhooks.List(ctx, org)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &webhookListOutput{Body: apitypes.NewList(hooks, "")}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getWebhook", Method: "GET", Path: "/api/v1/webhooks/{id}",
		Summary: "Get a webhook",
		Description: "Fetch one webhook by `id`. Requires the `manage` capability, and the webhook must belong to the caller's organization.\n\n" +
			"The signing secret is never returned. Errors: `404 not_found` if no webhook with that id is owned by the caller's organization; `403 forbidden` if the caller lacks the `manage` capability.",
		Tags: []string{"Webhooks"}, Middlewares: manage,
	}, func(ctx context.Context, in *webhookIDInput) (*webhookOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		hook, err := h.Webhooks.Get(ctx, org, in.ID)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &webhookOutput{Body: hook}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "updateWebhook", Method: "PATCH", Path: "/api/v1/webhooks/{id}",
		Summary: "Update a webhook",
		Description: "Update the webhook identified by `id` — its `url`, `events`, `sessionId` scope, `secret`, `customHeaders`, `retryPolicy`, or `active` flag. Requires the `manage` capability, and the webhook must belong to the caller's organization.\n\n" +
			"**Secret rotation.** Send a new `secret` to rotate it; omit the field to leave the stored secret unchanged. As elsewhere, the secret is never returned.\n\n" +
			"On success returns `200` with the updated webhook. Errors: `404 not_found` if no webhook with that id is owned by the caller's organization; `422 validation_error` if the supplied fields are invalid (e.g. a malformed `url`); `403 forbidden` if the caller lacks the `manage` capability.",
		Tags: []string{"Webhooks"}, Middlewares: manage,
	}, func(ctx context.Context, in *updateWebhookInput) (*webhookOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		hook, err := h.Webhooks.Update(ctx, org, in.ID, in.Body.toInput())
		if err != nil {
			return nil, humax.Err(err)
		}
		return &webhookOutput{Body: hook}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteWebhook", Method: "DELETE", Path: "/api/v1/webhooks/{id}",
		Summary: "Delete a webhook",
		Description: "Delete the webhook identified by `id`. Requires the `manage` capability, and the webhook must belong to the caller's organization.\n\n" +
			"After deletion no further events are delivered to its url; any deliveries already pending retry stop. The deletion is permanent — there is no soft-delete (to pause deliveries without removing the config, set `active: false` via update instead).\n\n" +
			"On success returns `204` with an empty body. Errors: `404 not_found` if no webhook with that id is owned by the caller's organization; `403 forbidden` if the caller lacks the `manage` capability.",
		Tags:          []string{"Webhooks"},
		DefaultStatus: 204, Middlewares: manage,
	}, func(ctx context.Context, in *webhookIDInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Webhooks.Delete(ctx, org, in.ID); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})
}
