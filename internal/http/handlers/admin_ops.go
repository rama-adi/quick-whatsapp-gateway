package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

type adminSessionInput struct {
	Session string `path:"session" doc:"Session ID. As super_admin this may belong to any organization." example:"01HZX9Q2K7"`
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
		Summary: "List sessions across all organizations (super_admin)", Tags: []string{"Admin"}, Middlewares: superAdmin,
		Description: "Return all sessions across all organizations for platform admins.\n\n" +
			"Read-only route.\n\n" +
			"Requires a login JWT with role `super_admin`.\n\n" +
			"Only login-based super admins are accepted. Api-keys and org roles return `forbidden` (403).\n\n" +
			"Returns a flat list of session rows.",
	}, func(ctx context.Context, _ *struct{}) (*adminSessionListOutput, error) {
		sessions, err := h.Admin.ListAllSessions(ctx)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &adminSessionListOutput{Body: apitypes.NewList(sessions, "")}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "adminStartSessionBackfill", Method: "POST", Path: "/api/v1/admin/sessions/{session}:backfill",
		Summary: "Start a session data backfill (super_admin)", Tags: []string{"Admin"},
		DefaultStatus: 202, Middlewares: superAdmin,
		Description: "Start one in-memory backfill job for a single session.\n\n" +
			"The job fetches contacts and joined group metadata/members. Chat history is not pulled by backfill.\n\n" +
			"Returns **202 Accepted** with job state. Poll `GET /admin/sessions/{session}/backfill` for progress.\n\n" +
			"Only one backfill can run per session; existing in-flight jobs return `conflict` (409).\n\n" +
			"Requires platform super_admin login JWT. Api-keys and org roles are rejected with `forbidden` (403).\n\n" +
			"Errors: `not_found` (404), `conflict` (409), `not_implemented` (501).",
	}, func(ctx context.Context, in *adminSessionInput) (*adminBackfillJobOutput, error) {
		job, err := h.Admin.StartBackfill(ctx, in.Session)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &adminBackfillJobOutput{Body: job}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "adminSessionBackfillStatus", Method: "GET", Path: "/api/v1/admin/sessions/{session}/backfill",
		Summary: "Get the current or latest session backfill job (super_admin)", Tags: []string{"Admin"}, Middlewares: superAdmin,
		Description: "Get the active backfill job for a session, or the most recent completed/failed one if none is running.\n\n" +
			"Backfill state is in-memory, so restarts clear job history.\n\n" +
			"Requires platform super_admin login JWT.\n\n" +
			"Errors: `forbidden` (403) and `not_found` (404) if session missing or no job exists.",
	}, func(ctx context.Context, in *adminSessionInput) (*adminBackfillJobOutput, error) {
		job, err := h.Admin.BackfillStatus(ctx, in.Session)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &adminBackfillJobOutput{Body: job}, nil
	})
}
