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
	SessionID     *string             `json:"sessionId,omitempty"`
	URL           string              `json:"url,omitempty"`
	Events        []string            `json:"events,omitempty"`
	Secret        *string             `json:"secret,omitempty"`
	CustomHeaders map[string]string   `json:"customHeaders,omitempty"`
	RetryPolicy   *domain.RetryPolicy `json:"retryPolicy,omitempty"`
	Active        *bool               `json:"active,omitempty"`
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
	ID   string `path:"id"`
	Body webhookBody
}
type webhookIDInput struct {
	ID string `path:"id"`
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
		Summary: "Create a webhook", Tags: []string{"Webhooks"},
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
		Summary: "List webhooks", Tags: []string{"Webhooks"}, Middlewares: manage,
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
		Summary: "Get a webhook", Tags: []string{"Webhooks"}, Middlewares: manage,
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
		Summary: "Update a webhook", Tags: []string{"Webhooks"}, Middlewares: manage,
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
		Summary: "Delete a webhook", Tags: []string{"Webhooks"},
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
