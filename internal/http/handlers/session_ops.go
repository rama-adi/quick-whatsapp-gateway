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

// createSessionInputBody is the POST /sessions request body. Fields are optional
// on the wire so the service stays the validator (domain.ErrValidation → 400),
// matching the chi handler's behavior.
type createSessionInputBody struct {
	Label          *string `json:"label,omitempty" doc:"Optional human-friendly session label." example:"Sales line"`
	Start          bool    `json:"start,omitempty" doc:"If true, begin QR pairing immediately. If false, create the session in stopped/unpaired state and pair later." example:"true"`
	AutoRead       *bool   `json:"autoRead,omitempty" doc:"If true, mark incoming messages as read automatically." example:"false"`
	PresenceTyping *bool   `json:"presenceTyping,omitempty" doc:"If true, send typing indicator while a message is sent from this session." example:"true"`
}

type createSessionInput struct{ Body createSessionInputBody }
type sessionIDInput struct {
	Session string `path:"session" doc:"Session ID within your organization. Unknown or cross-organization sessions return not_found (404)." example:"sess_01HZX8K3M9"`
}
type pairingCodeInput struct {
	Session string `path:"session" doc:"Session ID to generate a pairing code for. Must belong to your organization and be unpaired." example:"sess_01HZX8K3M9"`
	Body    struct {
		Phone string `json:"phone,omitempty" doc:"Phone number to pair in E.164 digits only (country code + number, no + or spaces)." example:"628123456789"`
	}
}

type sessionOutput struct{ Body domain.WASession }
type sessionListOutput struct {
	Body apitypes.List[domain.WASession]
}
type sessionMeOutput struct{ Body service.Me }
type sessionQROutput struct{ Body service.QR }
type pairingCodeOutput struct {
	Body struct {
		Code string `json:"code"`
	}
}

// RegisterSessionOps registers the session lifecycle operations (manage
// capability) on the huma API. Code-first replacement for the chi sessions group
// (the two /backfill routes remain on chi — they belong to the backup resource).
func RegisterSessionOps(api huma.API, h *Handlers) {
	manage := huma.Middlewares{humax.RequireCap(api, authz.CapManage)}

	huma.Register(api, huma.Operation{
		OperationID: "createSession", Method: "POST", Path: "/api/v1/sessions",
		Summary: "Create a session",
		Description: "Create a new session record for the caller's organization.\n\n" +
			"Behavior: session starts **unpaired**. Set `start` to true to begin pairing immediately; pairing then proceeds asynchronously.\n\n" +
			"Requires `manage` capability. Unknown or invalid request data returns `validation_error` (400). Auth errors are `unauthorized` (401) and `forbidden` (403).\n\n" +
			"Returns **201 Created** with the new session. This endpoint is not idempotent.",
		Tags:          []string{"Sessions"},
		DefaultStatus: 201, Middlewares: manage,
	}, func(ctx context.Context, in *createSessionInput) (*sessionOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		sess, err := h.Sessions.Create(ctx, org, service.CreateInput{
			Label:          in.Body.Label,
			Start:          in.Body.Start,
			AutoRead:       in.Body.AutoRead,
			PresenceTyping: in.Body.PresenceTyping,
		})
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sessionOutput{Body: sess}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "listSessions", Method: "GET", Path: "/api/v1/sessions",
		Summary: "List sessions",
		Description: "List all sessions in the caller's organization, including status, number if attached, and assigned gateway.\n\n" +
			"Requires `manage` capability.\n\n" +
			"Pagination is a compatibility field only: response includes `nextCursor` but sessions are currently returned in one page.\n\n" +
			"Errors: `unauthorized` (401) and `forbidden` (403).",
		Tags: []string{"Sessions"}, Middlewares: manage,
	}, func(ctx context.Context, _ *struct{}) (*sessionListOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		sessions, err := h.Sessions.List(ctx, org)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sessionListOutput{Body: apitypes.NewList(sessions, "")}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getSession", Method: "GET", Path: "/api/v1/sessions/{session}",
		Summary: "Get a session",
		Description: "Return one session by id with current status and attachment metadata.\n\n" +
			"Requires `manage` capability.\n\n" +
			"Cross-org session ids are returned as `not_found` (404).\n\n" +
			"Errors: `not_found` (404), `unauthorized` (401), `forbidden` (403).",
		Tags: []string{"Sessions"}, Middlewares: manage,
	}, func(ctx context.Context, in *sessionIDInput) (*sessionOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		sess, err := h.Sessions.Get(ctx, org, in.Session)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sessionOutput{Body: sess}, nil
	})

	// Lifecycle actions (colon-suffix). On success each returns the refreshed
	// session row so the client sees the new status, matching the chi handler.
	// The action is selected inside the closure (not as a method value here) so
	// registration never dereferences h.Sessions.
	registerSessionAction(api, h, manage, "startSession", "/api/v1/sessions/{session}:start", "Start a session",
		"Connect a paired session to WhatsApp.\n\n"+
			"Precondition: the session must already be paired and owned by your organization.\n\n"+
			"Returns updated session row. Calling start repeatedly is idempotent.\n\n"+
			"Errors: `not_found` (404), `unauthorized` (401), `forbidden` (403).",
		func(ctx context.Context, org, id string) error { return h.Sessions.Start(ctx, org, id) })
	registerSessionAction(api, h, manage, "stopSession", "/api/v1/sessions/{session}:stop", "Stop a session",
		"Disconnect a paired session from WhatsApp while keeping device pairing in place.\n\n"+
			"Use `:logout` to fully unlink the device and clear attachment.\n\n"+
			"Response is the refreshed session row. Repeating stop is safe and idempotent.\n\n"+
			"Errors: `not_found` (404), `unauthorized` (401), `forbidden` (403).",
		func(ctx context.Context, org, id string) error { return h.Sessions.Stop(ctx, org, id) })
	registerSessionAction(api, h, manage, "restartSession", "/api/v1/sessions/{session}:restart", "Restart a session",
		"Reconnect a session by performing stop then start while keeping its current attachment.\n\n"+
			"Use this for transient failed/reconnecting states. The action is idempotent.\n\n"+
			"Errors: `not_found` (404), `unauthorized` (401), `forbidden` (403).",
		func(ctx context.Context, org, id string) error { return h.Sessions.Restart(ctx, org, id) })
	registerSessionAction(api, h, manage, "logoutSession", "/api/v1/sessions/{session}:logout", "Log out a session (unpair the device)",
		"Unlink the device and clear the attached WhatsApp number.\n\n"+
			"To delete session metadata, call `DELETE /sessions/{session}`.\n\n"+
			"Response is refreshed session row; repeated calls are idempotent when already logged out.\n\n"+
			"Errors: `not_found` (404), `unauthorized` (401), `forbidden` (403).",
		func(ctx context.Context, org, id string) error { return h.Sessions.Logout(ctx, org, id) })

	huma.Register(api, huma.Operation{
		OperationID: "deleteSession", Method: "DELETE", Path: "/api/v1/sessions/{session}",
		Summary: "Delete a session",
		Description: "Permanently remove the session row and related session state.\n\n" +
			"If a number is still attached, call `:logout` first for clean detachment.\n\n" +
			"Requires `manage` capability.\n\n" +
			"Errors: `not_found` (404), `unauthorized` (401), `forbidden` (403).\n\n" +
			"Returns **204 No Content** on success.",
		Tags:          []string{"Sessions"},
		DefaultStatus: 204, Middlewares: manage,
	}, func(ctx context.Context, in *sessionIDInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Sessions.Delete(ctx, org, in.Session); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessionMe", Method: "GET", Path: "/api/v1/sessions/{session}/me",
		Summary: "Get the connected account's own identity",
		Description: "Return identity details for the WhatsApp account attached to this session (JID, device id, push name, phone).\n\n" +
			"Requires `manage` capability and an attached/pairing session.\n\n" +
			"Errors: `not_found` (404), `not_implemented` (501), `unauthorized` (401), `forbidden` (403).",
		Tags: []string{"Sessions"}, Middlewares: manage,
	}, func(ctx context.Context, in *sessionIDInput) (*sessionMeOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		me, err := h.Sessions.Me(ctx, org, in.Session)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sessionMeOutput{Body: me}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessionQR", Method: "GET", Path: "/api/v1/sessions/{session}/qr",
		Summary: "Get the current pairing QR code",
		Description: "Return the current pairing QR code for the session.\n\n" +
			"If a code is not ready yet, pairing starts and the route returns `not_found` (404) until one is available.\n\n" +
			"Requires `manage` capability. Response body is raw QR string.\n\n" +
			"Errors: `not_found` (404), `unauthorized` (401), `forbidden` (403).",
		Tags: []string{"Sessions"}, Middlewares: manage,
	}, func(ctx context.Context, in *sessionIDInput) (*sessionQROutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		qr, err := h.Sessions.QR(ctx, org, in.Session)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sessionQROutput{Body: qr}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessionPairingCode", Method: "POST", Path: "/api/v1/sessions/{session}/pairing-code",
		Summary: "Request a phone pairing code",
		Description: "Get a phone pairing code so the session can be linked without QR scanning.\n\n" +
			"Pass `phone` in E.164 digits (no plus sign). The returned code is short-lived and must be used from the phone.\n\n" +
			"Requires `manage` capability; session must be unpaired.\n\n" +
			"Errors: `validation_error` (400), `not_found` (404), `unauthorized` (401), `forbidden` (403).",
		Tags: []string{"Sessions"}, Middlewares: manage,
	}, func(ctx context.Context, in *pairingCodeInput) (*pairingCodeOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		code, err := h.Sessions.PairingCode(ctx, org, in.Session, in.Body.Phone)
		if err != nil {
			return nil, humax.Err(err)
		}
		out := &pairingCodeOutput{}
		out.Body.Code = code
		return out, nil
	})
}

// registerSessionAction wires a no-payload lifecycle action (:start, :stop,
// :restart, :logout): it calls fn then returns the refreshed session row.
func registerSessionAction(api huma.API, h *Handlers, mw huma.Middlewares, opID, path, summary, description string, fn func(ctx context.Context, organizationID, id string) error) {
	huma.Register(api, huma.Operation{
		OperationID: opID, Method: "POST", Path: path,
		Summary: summary, Description: description, Tags: []string{"Sessions"}, Middlewares: mw,
	}, func(ctx context.Context, in *sessionIDInput) (*sessionOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := fn(ctx, org, in.Session); err != nil {
			return nil, humax.Err(err)
		}
		sess, err := h.Sessions.Get(ctx, org, in.Session)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sessionOutput{Body: sess}, nil
	})
}
