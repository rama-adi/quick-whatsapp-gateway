package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

type adminSessionInput struct {
	Session string `path:"session" doc:"The WhatsApp session id to target. A session is one attached WhatsApp number; the id is the opaque session identifier returned by the session list/create endpoints (it is **not** the phone number). As a super_admin endpoint this may name a session owned by any organization, not only the caller's." example:"01HZX9Q2K7"`
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
		Description: `List WhatsApp sessions across **every** organization, for platform oversight.

Unlike the org-scoped session list, this is not limited to the caller's organization — it returns sessions for all tenants in one flat list.

### Preconditions

- Restricted to a platform **super_admin**. The caller must present a login JWT that carries the ` + "`super_admin`" + ` platform role.
- api-keys (prefixed ` + "`wa_`" + `) and ordinary org roles (owner/admin/member) are **rejected** even if otherwise valid.

### Side effects & semantics

- Read-only. No state is changed.

### Errors

- ` + "`forbidden`" + ` (403) — the caller is authenticated but is not a platform super_admin (e.g. an api-key, or a person without the super_admin role).`,
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
		Description: `Start an in-memory background backfill for one WhatsApp session, re-pulling the directly fetchable WhatsApp data for that number.

The job pulls only the data WhatsApp exposes through a direct fetch: **cached contacts** and **joined group metadata/members**. Ordinary chat history is **not** part of a backfill — message history arrives asynchronously through WhatsApp ` + "`HistorySync`" + ` events, and there is no generic "fetch all messages" API.

### Asynchronous behavior

- Returns **202 Accepted** immediately with the created/running ` + "`BackfillJob`" + `; the work continues in the background.
- Poll ` + "`GET /admin/sessions/{session}/backfill`" + ` to observe progress and completion.

### Idempotency / concurrency

- At most **one** backfill may run per session at a time. If a backfill is already in progress for this session, the request is rejected with **409** rather than starting a second one — it is **not** a no-op merge into the existing job.

### Preconditions

- Requires the platform **super_admin** role (login JWT only; api-keys and org roles are rejected with 403).
- The session must exist.

### Errors

- ` + "`forbidden`" + ` (403) — caller is not a platform super_admin.
- ` + "`not_found`" + ` (404) — no session with the given id exists.
- ` + "`conflict`" + ` (409) — a backfill is already running for this session; wait for it to finish (poll the status endpoint) before retrying.
- ` + "`not_implemented`" + ` (501) — backfill is unavailable in this build/configuration for the requested session.`,
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
		Description: `Return the **current or most recent** backfill job for one WhatsApp session.

Use this to poll a backfill started with ` + "`POST /admin/sessions/{session}:backfill`" + `: it reports the job's status and progress. If a backfill is in progress, the running job is returned; otherwise the last completed/failed job for the session is returned.

### Semantics

- Read-only. Backfill state is held **in memory**, so a gateway restart clears it: after a restart this returns 404 until a new backfill is started.

### Preconditions

- Requires the platform **super_admin** role (login JWT only; api-keys and org roles are rejected with 403).

### Errors

- ` + "`forbidden`" + ` (403) — caller is not a platform super_admin.
- ` + "`not_found`" + ` (404) — the session does not exist, or no backfill job is on record for it (e.g. none has been started since the last gateway restart).`,
	}, func(ctx context.Context, in *adminSessionInput) (*adminBackfillJobOutput, error) {
		job, err := h.Admin.BackfillStatus(ctx, in.Session)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &adminBackfillJobOutput{Body: job}, nil
	})
}
