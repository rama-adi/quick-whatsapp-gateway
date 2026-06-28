package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

type adminSessionInput struct {
	Session string `path:"session"`
}

type adminSessionListOutput struct {
	Body apitypes.List[domain.WASession]
}

type adminBackfillJobOutput struct {
	Body domain.BackfillJob
}

// RegisterAdminOps registers the super_admin cross-organization oversight
// operations (§4.3) on the huma API. Code-first replacement for the chi admin
// group.
func RegisterAdminOps(api huma.API, h *Handlers) {
	superAdmin := huma.Middlewares{humax.RequireSuperAdmin(api)}

	huma.Register(api, huma.Operation{
		OperationID: "adminListSessions", Method: "GET", Path: "/api/v1/admin/sessions",
		Summary: "List all sessions (admin)", Tags: []string{"Admin"}, Middlewares: superAdmin,
	}, func(ctx context.Context, _ *struct{}) (*adminSessionListOutput, error) {
		sessions, err := h.Admin.ListAllSessions(ctx)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &adminSessionListOutput{Body: apitypes.NewList(sessions, "")}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "adminStartSessionBackfill", Method: "POST", Path: "/api/v1/admin/sessions/{session}:backfill",
		Summary: "Start a session backfill (admin)", Tags: []string{"Admin"},
		DefaultStatus: 202, Middlewares: superAdmin,
	}, func(ctx context.Context, in *adminSessionInput) (*adminBackfillJobOutput, error) {
		job, err := h.Admin.StartBackfill(ctx, in.Session)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &adminBackfillJobOutput{Body: job}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "adminSessionBackfillStatus", Method: "GET", Path: "/api/v1/admin/sessions/{session}/backfill",
		Summary: "Get session backfill status (admin)", Tags: []string{"Admin"}, Middlewares: superAdmin,
	}, func(ctx context.Context, in *adminSessionInput) (*adminBackfillJobOutput, error) {
		job, err := h.Admin.BackfillStatus(ctx, in.Session)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &adminBackfillJobOutput{Body: job}, nil
	})
}
