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
	Label          *string `json:"label,omitempty"`
	Start          bool    `json:"start,omitempty"`
	AutoRead       *bool   `json:"autoRead,omitempty"`
	PresenceTyping *bool   `json:"presenceTyping,omitempty"`
}

type createSessionInput struct{ Body createSessionInputBody }
type sessionIDInput struct {
	Session string `path:"session"`
}
type pairingCodeInput struct {
	Session string `path:"session"`
	Body    struct {
		Phone string `json:"phone,omitempty"`
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
		Summary: "Create a session", Tags: []string{"Sessions"},
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
		Summary: "List sessions", Tags: []string{"Sessions"}, Middlewares: manage,
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
		Summary: "Get a session", Tags: []string{"Sessions"}, Middlewares: manage,
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
		func(ctx context.Context, org, id string) error { return h.Sessions.Start(ctx, org, id) })
	registerSessionAction(api, h, manage, "stopSession", "/api/v1/sessions/{session}:stop", "Stop a session",
		func(ctx context.Context, org, id string) error { return h.Sessions.Stop(ctx, org, id) })
	registerSessionAction(api, h, manage, "restartSession", "/api/v1/sessions/{session}:restart", "Restart a session",
		func(ctx context.Context, org, id string) error { return h.Sessions.Restart(ctx, org, id) })
	registerSessionAction(api, h, manage, "logoutSession", "/api/v1/sessions/{session}:logout", "Log out a session",
		func(ctx context.Context, org, id string) error { return h.Sessions.Logout(ctx, org, id) })

	huma.Register(api, huma.Operation{
		OperationID: "deleteSession", Method: "DELETE", Path: "/api/v1/sessions/{session}",
		Summary: "Delete a session", Tags: []string{"Sessions"},
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
		Summary: "Get the attached WhatsApp identity", Tags: []string{"Sessions"}, Middlewares: manage,
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
		Summary: "Get the current pairing QR code", Tags: []string{"Sessions"}, Middlewares: manage,
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
		Summary: "Request a pairing code", Tags: []string{"Sessions"}, Middlewares: manage,
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
func registerSessionAction(api huma.API, h *Handlers, mw huma.Middlewares, opID, path, summary string, fn func(ctx context.Context, organizationID, id string) error) {
	huma.Register(api, huma.Operation{
		OperationID: opID, Method: "POST", Path: path,
		Summary: summary, Tags: []string{"Sessions"}, Middlewares: mw,
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
