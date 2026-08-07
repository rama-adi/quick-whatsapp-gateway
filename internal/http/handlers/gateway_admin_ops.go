package handlers

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service/gatewayadmin"
)

type gatewayAdminBody struct {
	Label    *string `json:"label,omitempty" maxLength:"255" doc:"Optional operator-facing gateway name." example:"Singapore primary"`
	Notes    *string `json:"notes,omitempty" doc:"Optional operator notes. Never sent to gateway processes." example:"Runs the APAC production pool."`
	Capacity *int    `json:"capacity,omitempty" minimum:"0" doc:"Optional soft cap on placed sessions. Null or omitted means no cap." example:"100"`
}

type createGatewayAdminInput struct{ Body gatewayAdminBody }
type gatewayAdminIDInput struct {
	GatewayID string `path:"gatewayId" doc:"Gateway identifier." example:"gw_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
}
type deleteGatewayAdminInput struct {
	GatewayID string `path:"gatewayId" doc:"Gateway identifier." example:"gw_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Body      struct {
		ConsequencesAcknowledged bool `json:"consequencesAcknowledged" doc:"Must be true. Confirms that the gateway is drained/disabled and its session/device consequences were reviewed." example:"true"`
	}
}

type gatewayAdminOutput struct{ Body apitypes.GatewayAdminDetail }
type gatewayAdminListOutput struct {
	Body apitypes.List[apitypes.GatewayAdmin]
}
type gatewayEnrollmentOutput struct {
	Body apitypes.GatewayEnrollmentResult
}
type gatewayAdminEmptyOutput struct{}

// RegisterGatewayAdminOps exposes the API-local gateway administration control
// plane. These routes are deliberately mounted at the router, never reverse
// proxied to a gateway; the middleware accepts only platform super_admin JWTs.
func RegisterGatewayAdminOps(api huma.API, h *Handlers) {
	superAdmin := huma.Middlewares{humax.RequireSuperAdmin(api)}

	huma.Register(api, huma.Operation{
		OperationID: "createAdminGateway", Method: "POST", Path: "/api/v1/admin/gateways",
		Summary: "Create a gateway and return its one-time enrollment token (super_admin)", Tags: []string{"Gateway Administration"},
		Description:   "Create a gateway registry row and issue a single-use enrollment bearer. The plaintext token is returned only in this response; it is never available from list/detail/audit APIs. Requires a login JWT with platform role `super_admin`; api-keys are rejected.",
		DefaultStatus: 201, Middlewares: superAdmin,
	}, func(ctx context.Context, in *createGatewayAdminInput) (*gatewayEnrollmentOutput, error) {
		actor, err := gatewayAdminActor(ctx)
		if err != nil {
			return nil, err
		}
		issued, err := h.GatewayAdmin.CreateGateway(ctx, gatewayadmin.CreateGatewayInput{Label: in.Body.Label, Notes: in.Body.Notes, Capacity: in.Body.Capacity, Actor: actor})
		if err != nil {
			return nil, gatewayAdminError(ctx, err)
		}
		return &gatewayEnrollmentOutput{Body: enrollmentResult(issued)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "listAdminGateways", Method: "GET", Path: "/api/v1/admin/gateways",
		Summary: "List gateways (super_admin)", Tags: []string{"Gateway Administration"},
		Description: "List every non-deleted gateway with non-secret registry and reconciliation metadata. Requires a login JWT with platform role `super_admin`; api-keys are rejected.", Middlewares: superAdmin,
	}, func(ctx context.Context, _ *struct{}) (*gatewayAdminListOutput, error) {
		gateways, err := h.GatewayAdmin.ListGateways(ctx)
		if err != nil {
			return nil, gatewayAdminError(ctx, err)
		}
		out := make([]apitypes.GatewayAdmin, 0, len(gateways))
		for _, gateway := range gateways {
			out = append(out, apitypes.GatewayAdminFromDomain(gateway))
		}
		return &gatewayAdminListOutput{Body: apitypes.NewList(out, "")}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getAdminGateway", Method: "GET", Path: "/api/v1/admin/gateways/{gatewayId}",
		Summary: "Get gateway administration detail (super_admin)", Tags: []string{"Gateway Administration"},
		Description: "Get non-secret gateway metadata, assigned sessions, certificate summaries, and recent audit entries. Requires platform `super_admin`.", Middlewares: superAdmin,
	}, func(ctx context.Context, in *gatewayAdminIDInput) (*gatewayAdminOutput, error) {
		detail, err := h.GatewayAdmin.GetGateway(ctx, in.GatewayID)
		if err != nil {
			return nil, gatewayAdminError(ctx, err)
		}
		return &gatewayAdminOutput{Body: apitypes.GatewayAdminDetailFromDomain(detail)}, nil
	})

	registerGatewayEnrollmentAction(api, h, "replaceAdminGatewayEnrollmentToken", ":replace-enrollment-token", "Replace an unredeemed enrollment token", func(ctx context.Context, id string, actor gatewayadmin.Actor) (gatewayadmin.IssuedEnrollment, error) {
		return h.GatewayAdmin.ReplaceEnrollmentToken(ctx, id, actor)
	})
	registerGatewayEnrollmentAction(api, h, "reenrollAdminGateway", ":reenroll", "Re-enroll a drained or disabled gateway", func(ctx context.Context, id string, actor gatewayadmin.Actor) (gatewayadmin.IssuedEnrollment, error) {
		return h.GatewayAdmin.Reenroll(ctx, id, actor)
	})
	registerGatewayAction(api, h, "drainAdminGateway", ":drain", "Drain a gateway", func(ctx context.Context, id string, actor gatewayadmin.Actor) error {
		return h.GatewayAdmin.Drain(ctx, id, actor)
	})
	registerGatewayAction(api, h, "resumeAdminGateway", ":resume", "Resume placement on a drained gateway", func(ctx context.Context, id string, actor gatewayadmin.Actor) error {
		return h.GatewayAdmin.Resume(ctx, id, actor)
	})
	registerGatewayAction(api, h, "disableAdminGateway", ":disable", "Disable a gateway", func(ctx context.Context, id string, actor gatewayadmin.Actor) error {
		return h.GatewayAdmin.Disable(ctx, id, actor)
	})
	registerGatewayAction(api, h, "reenableAdminGateway", ":reenable", "Re-enable a gateway", func(ctx context.Context, id string, actor gatewayadmin.Actor) error {
		return h.GatewayAdmin.Reenable(ctx, id, actor)
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteAdminGateway", Method: "DELETE", Path: "/api/v1/admin/gateways/{gatewayId}",
		Summary: "Safely delete a gateway (super_admin)", Tags: []string{"Gateway Administration"},
		Description:   "Soft-delete a gateway only after it is drained or disabled, has no assigned sessions, no active enrollment token, and no usable certificate. `consequencesAcknowledged` must be true. Invalid or unsafe state returns `conflict` without revealing whether the gateway exists. Requires platform `super_admin`.",
		DefaultStatus: 204, Middlewares: superAdmin,
	}, func(ctx context.Context, in *deleteGatewayAdminInput) (*gatewayAdminEmptyOutput, error) {
		actor, err := gatewayAdminActor(ctx)
		if err != nil {
			return nil, err
		}
		if err = h.GatewayAdmin.Delete(ctx, in.GatewayID, in.Body.ConsequencesAcknowledged, actor); err != nil {
			return nil, gatewayAdminError(ctx, err)
		}
		return nil, nil
	})
}

func registerGatewayEnrollmentAction(api huma.API, h *Handlers, operationID, suffix, summary string, action func(context.Context, string, gatewayadmin.Actor) (gatewayadmin.IssuedEnrollment, error)) {
	huma.Register(api, huma.Operation{OperationID: operationID, Method: "POST", Path: "/api/v1/admin/gateways/{gatewayId}" + suffix, Summary: summary + " (super_admin)", Tags: []string{"Gateway Administration"}, Description: "Returns a plaintext single-use enrollment bearer exactly once. Requires platform `super_admin`; invalid lifecycle state returns `conflict`.", DefaultStatus: 201, Middlewares: huma.Middlewares{humax.RequireSuperAdmin(api)}}, func(ctx context.Context, in *gatewayAdminIDInput) (*gatewayEnrollmentOutput, error) {
		actor, err := gatewayAdminActor(ctx)
		if err != nil {
			return nil, err
		}
		issued, err := action(ctx, in.GatewayID, actor)
		if err != nil {
			return nil, gatewayAdminError(ctx, err)
		}
		return &gatewayEnrollmentOutput{Body: enrollmentResult(issued)}, nil
	})
}

func registerGatewayAction(api huma.API, h *Handlers, operationID, suffix, summary string, action func(context.Context, string, gatewayadmin.Actor) error) {
	huma.Register(api, huma.Operation{OperationID: operationID, Method: "POST", Path: "/api/v1/admin/gateways/{gatewayId}" + suffix, Summary: summary + " (super_admin)", Tags: []string{"Gateway Administration"}, Description: "Changes the gateway's durable administrative state and records a non-secret audit event. Requires platform `super_admin`; invalid lifecycle state returns `conflict`.", DefaultStatus: 204, Middlewares: huma.Middlewares{humax.RequireSuperAdmin(api)}}, func(ctx context.Context, in *gatewayAdminIDInput) (*gatewayAdminEmptyOutput, error) {
		actor, err := gatewayAdminActor(ctx)
		if err != nil {
			return nil, err
		}
		if err = action(ctx, in.GatewayID, actor); err != nil {
			return nil, gatewayAdminError(ctx, err)
		}
		return nil, nil
	})
}

func gatewayAdminActor(ctx context.Context) (gatewayadmin.Actor, error) {
	p, err := humax.Principal(ctx)
	if err != nil {
		return gatewayadmin.Actor{}, err
	}
	return gatewayadmin.Actor{UserID: p.UserID, RequestID: httpx.RequestID(ctx)}, nil
}

func enrollmentResult(issued gatewayadmin.IssuedEnrollment) apitypes.GatewayEnrollmentResult {
	return apitypes.GatewayEnrollmentResult{GatewayID: issued.GatewayID, TokenID: issued.TokenID, Token: issued.Token, ExpiresAt: issued.ExpiresAt}
}

func gatewayAdminError(ctx context.Context, err error) error {
	if errors.Is(err, gatewayadmin.ErrDenied) {
		return humax.ErrContext(ctx, domain.ErrForbidden("super_admin required"))
	}
	var conflict *gatewayadmin.StateConflictError
	if errors.As(err, &conflict) {
		return humax.ErrContext(ctx, domain.ErrConflict("gateway administration state conflict"))
	}
	return humax.ErrContext(ctx, err)
}
