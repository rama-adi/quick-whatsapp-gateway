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
	Label          *string `json:"label,omitempty" doc:"Optional human-friendly name for the session, shown in dashboards and lists. Omit to create an unlabeled session." example:"Sales line"`
	Start          bool    `json:"start,omitempty" doc:"If true, begin QR pairing immediately on creation (the session transitions toward connecting and a QR code becomes available). If false (the default), the session is created stopped/unpaired and you start pairing later via the QR or pairing-code endpoints." example:"true"`
	AutoRead       *bool   `json:"autoRead,omitempty" doc:"If true, incoming messages on this session are automatically marked as read (blue ticks). Optional; when omitted the gateway default applies." example:"false"`
	PresenceTyping *bool   `json:"presenceTyping,omitempty" doc:"If true, the session emits a \"typing…\" presence indicator to the recipient while it sends a message. Optional; when omitted the gateway default applies." example:"true"`
}

type createSessionInput struct{ Body createSessionInputBody }
type sessionIDInput struct {
	Session string `path:"session" doc:"The session id — a session is one attached (or to-be-attached) WhatsApp number, scoped to your organization. A session in another organization is reported as not_found (404), never forbidden." example:"sess_01HZX8K3M9"`
}
type pairingCodeInput struct {
	Session string `path:"session" doc:"The session id to pair. Must be an existing, not-yet-paired session in your organization; a session in another organization is reported as not_found (404)." example:"sess_01HZX8K3M9"`
	Body    struct {
		Phone string `json:"phone,omitempty" doc:"The phone number to pair, in international format with country code and no leading + or spaces (E.164 digits only). This is the WhatsApp number the code will be entered on under \"Link a device\"." example:"628123456789"`
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
		Description: `Creates a new session — a slot for one WhatsApp number — in the caller's organization.

Requires the ` + "`manage`" + ` capability.

The session starts **unpaired**: no WhatsApp number is attached yet. To attach a number, link a device by scanning the QR code (` + "`GET /sessions/{session}/qr`" + `) or by requesting a phone pairing code (` + "`POST /sessions/{session}/pairing-code`" + `).

**Side effects / async behavior.** When the request body sets ` + "`start: true`" + `, the gateway begins QR pairing right away and the returned session reflects the early connecting state; pairing then proceeds asynchronously (watch the event stream for ` + "`auth.qr`" + ` and connection events). When ` + "`start`" + ` is omitted or false, the session is created stopped.

**Not idempotent:** each call creates a distinct session.

**Errors.** ` + "`validation_error`" + ` (400) — the body failed validation (e.g. an invalid field). ` + "`unauthorized`" + ` (401) — missing or invalid credentials. ` + "`forbidden`" + ` (403) — the caller lacks the ` + "`manage`" + ` capability.

On success returns **201 Created** with the new session row.`,
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
		Description: `Lists the sessions (attached WhatsApp numbers) belonging to the caller's organization.

Requires the ` + "`manage`" + ` capability.

Each item shows the session's current status, the attached number once paired, and which gateway currently holds it. The response is org-scoped — sessions in other organizations are never returned.

**Pagination.** This list is returned in a single page (no cursor is consumed); the response carries a ` + "`nextCursor`" + ` field for forward compatibility, which is null when there are no further pages.

**Errors.** ` + "`unauthorized`" + ` (401) — missing or invalid credentials. ` + "`forbidden`" + ` (403) — the caller lacks the ` + "`manage`" + ` capability.`,
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
		Description: `Returns one session by id, including its current status and — once paired — the attached WhatsApp number and the gateway that holds it.

Requires the ` + "`manage`" + ` capability.

**Org isolation.** A session that belongs to another organization is reported as ` + "`not_found`" + ` (404), **not** ` + "`forbidden`" + `, so a caller cannot probe for the existence of other orgs' sessions.

**Errors.** ` + "`not_found`" + ` (404) — no such session in your organization. ` + "`unauthorized`" + ` (401) / ` + "`forbidden`" + ` (403) — credential / capability failures.`,
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
		`Connects an **already-paired** session to WhatsApp.

Requires the `+"`manage`"+` capability.

**Precondition.** The session must already have a number attached. To pair a *new* session instead, use the QR (`+"`GET /sessions/{session}/qr`"+`) or pairing-code (`+"`POST /sessions/{session}/pairing-code`"+`) endpoints.

**Async / idempotency.** Connecting happens in the background; the response returns the refreshed session row so you can observe its new status (it may still be transitioning). Calling start on a session that is already started is a harmless no-op.

**Errors.** `+"`not_found`"+` (404) — no such session in your organization. `+"`unauthorized`"+` (401) / `+"`forbidden`"+` (403) — credential / capability failures.

Returns **200** with the refreshed session.`,
		func(ctx context.Context, org, id string) error { return h.Sessions.Start(ctx, org, id) })
	registerSessionAction(api, h, manage, "stopSession", "/api/v1/sessions/{session}:stop", "Stop a session",
		`Disconnects a session from WhatsApp **without** unlinking the device.

Requires the `+"`manage`"+` capability.

The attached number stays paired, so you can `+"`:start`"+` it again later without re-pairing. To unlink the device entirely, use `+"`:logout`"+` instead.

**Idempotency.** Stopping an already-stopped session is a harmless no-op. The response returns the refreshed session so you can see its new status.

**Errors.** `+"`not_found`"+` (404) — no such session in your organization. `+"`unauthorized`"+` (401) / `+"`forbidden`"+` (403) — credential / capability failures.

Returns **200** with the refreshed session.`,
		func(ctx context.Context, org, id string) error { return h.Sessions.Stop(ctx, org, id) })
	registerSessionAction(api, h, manage, "restartSession", "/api/v1/sessions/{session}:restart", "Restart a session",
		`Stops and then starts the session — a reconnect that keeps the number paired.

Requires the `+"`manage`"+` capability.

Use this to recover a session that is stuck or in a failed state. The number stays attached throughout; no re-pairing is needed.

**Async.** The reconnect proceeds in the background; the response returns the refreshed session row so you can observe its new status.

**Errors.** `+"`not_found`"+` (404) — no such session in your organization. `+"`unauthorized`"+` (401) / `+"`forbidden`"+` (403) — credential / capability failures.

Returns **200** with the refreshed session.`,
		func(ctx context.Context, org, id string) error { return h.Sessions.Restart(ctx, org, id) })
	registerSessionAction(api, h, manage, "logoutSession", "/api/v1/sessions/{session}:logout", "Log out a session (unpair the device)",
		`Logs the session out of WhatsApp and **unlinks the device**, so the number is no longer attached.

Requires the `+"`manage`"+` capability.

**Destructive.** Unlike `+"`:stop`"+` (which keeps the pairing), logout severs the link: to use the number again you must pair it from scratch via QR or a pairing code. The session row itself remains — use `+"`DELETE /sessions/{session}`"+` to remove it entirely.

**Idempotency.** Logging out a session that is already logged out / unpaired is a harmless no-op. The response returns the refreshed session so you can see its new status.

**Errors.** `+"`not_found`"+` (404) — no such session in your organization. `+"`unauthorized`"+` (401) / `+"`forbidden`"+` (403) — credential / capability failures.

Returns **200** with the refreshed session.`,
		func(ctx context.Context, org, id string) error { return h.Sessions.Logout(ctx, org, id) })

	huma.Register(api, huma.Operation{
		OperationID: "deleteSession", Method: "DELETE", Path: "/api/v1/sessions/{session}",
		Summary: "Delete a session",
		Description: `Permanently deletes a session and its stored data.

Requires the ` + "`manage`" + ` capability.

**Destructive and irreversible.** This removes the session row for good. If a WhatsApp number is still attached, log it out first (` + "`POST /sessions/{session}:logout`" + `) to unlink the device cleanly; deleting does not guarantee the device is unlinked on WhatsApp's side.

**Idempotency.** Deleting a session that does not exist (or belongs to another org) returns ` + "`not_found`" + ` (404).

**Errors.** ` + "`not_found`" + ` (404) — no such session in your organization. ` + "`unauthorized`" + ` (401) / ` + "`forbidden`" + ` (403) — credential / capability failures.

Returns **204 No Content** on success.`,
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
		Description: `Returns the WhatsApp identity of the number attached to this session — its JID, linked-device id, push name, and phone number.

Requires the ` + "`manage`" + ` capability.

**Precondition.** The session must be paired; if no number is attached yet, this returns ` + "`not_found`" + ` (404). On builds where this is not yet wired, it may return ` + "`not_implemented`" + ` (501).

**Errors.** ` + "`not_found`" + ` (404) — session unknown or not yet paired. ` + "`not_implemented`" + ` (501) — the feature is stubbed in this build. ` + "`unauthorized`" + ` (401) / ` + "`forbidden`" + ` (403) — credential / capability failures.`,
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
		Description: `Returns the QR code to scan from WhatsApp on the phone (Settings → Linked devices → Link a device) to attach a number to this session.

Requires the ` + "`manage`" + ` capability.

**Behavior.** Returns the raw QR code string in the JSON body.

**Side effect.** If no code is ready yet, this endpoint *starts pairing* and returns ` + "`not_found`" + ` (404) until a code exists. QR codes are short-lived and refresh periodically; they also stream live over the event stream as ` + "`auth.qr`" + ` events, so the recommended pattern is to subscribe there and follow the codes as they rotate rather than polling.

**Precondition.** The session must be unpaired — requesting a QR for an already-paired session returns an error.

**Errors.** ` + "`not_found`" + ` (404) — no code ready yet (pairing was started) or session unknown. ` + "`unauthorized`" + ` (401) / ` + "`forbidden`" + ` (403) — credential / capability failures.`,
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
		Description: `Requests a pairing code to attach a number **without scanning a QR** — the alternative to the QR endpoint.

Requires the ` + "`manage`" + ` capability.

**Input.** Pass the target ` + "`phone`" + ` (international format, digits only, no leading + — e.g. ` + "`628123456789`" + `) in the request body.

**Output.** The response returns a short code such as ` + "`ABCD-1234`" + ` to enter in WhatsApp on that phone under Settings → Linked devices → Link a device → Link with phone number instead.

**Precondition.** The session must be unpaired; requesting a code for an already-paired session returns an error. The code is short-lived — request a fresh one if it expires before use.

**Errors.** ` + "`validation_error`" + ` (400) — missing or malformed ` + "`phone`" + `, or the session is already paired. ` + "`not_found`" + ` (404) — no such session in your organization. ` + "`unauthorized`" + ` (401) / ` + "`forbidden`" + ` (403) — credential / capability failures.`,
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
