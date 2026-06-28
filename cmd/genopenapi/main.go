// Command genopenapi generates the OpenAPI contract from the shared Go types
// (docs/plans/plan-router-impl.md D11): it builds one huma API from every resource
// registrar (without running a server), serializes the spec to YAML, and writes it
// to the output path (default docs/openapi.yaml). `make openapi` runs this;
// `make openapi-check` adds `git diff --exit-code` so the committed contract can
// never drift from the Go types.
//
// docs/openapi.yaml is a GENERATED, committed artifact (the hand-written source was
// retired at the huma cutover): to change the API, edit the Go input/output structs
// + operation metadata, then `make openapi` and regenerate the web client/docs
// (`pnpm gen:api`, `pnpm docs:openapi`).
package main

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

func main() {
	out := "docs/openapi.yaml"
	if len(os.Args) > 1 && os.Args[1] != "" {
		out = os.Args[1]
	}

	api := humax.NewSpecAPI()
	// Register every converted resource's operations. Each RegisterXOps is a pure
	// function of the huma API, so the generator assembles the unified spec without
	// a live server or real service dependencies.
	handlers.RegisterAllOps(api, &handlers.Handlers{})

	// Register the typed event catalog as an OpenAPI 3.1 `webhooks` section so
	// webhook receivers (and the realtime WebSocket client) get a fully typed,
	// discriminated shape per event type (D11, task #9).
	registerEventWebhooks(api)

	// Spec-level metadata: the API overview, servers, security schemes, and tag
	// descriptions (huma's defaults only set a bare title + version).
	decorateSpec(api)

	spec, err := api.OpenAPI().YAML()
	if err != nil {
		fmt.Fprintf(os.Stderr, "genopenapi: serialize spec: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, spec, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "genopenapi: write %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", out, len(spec))
}

// decorateSpec sets the document-level metadata that describes the API as a whole:
// the overview (info.description), the base server, the two security schemes (the
// better-auth JWT and api-key the central router accepts), the global security
// requirement, and a description for every tag. This is the API's front-page
// documentation — without it the generated contract is a bag of endpoints with no
// orientation for a new reader.
func decorateSpec(api huma.API) {
	o := api.OpenAPI()

	o.Info.Title = "WhatsApp Gateway API"
	o.Info.Version = "1.0.0"
	o.Info.Description = strings.TrimSpace(`
REST API for the WhatsApp gateway. Every session (one attached WhatsApp number)
belongs to one organization, and a caller only ever sees the sessions of the
organization it is acting as. This document is the contract of record — it is
**generated from the gateway's Go types** and served as-is at
` + "`/api/v1/openapi.yaml`" + ` by the central router (there is no Swagger UI).

**One front door.** Clients talk to the central router at a single base URL; the
router authenticates the caller, finds the gateway that owns the target session,
and proxies the request. Host discovery is never the caller's problem.

**Base path.** All endpoints below are under ` + "`/api/v1`" + `, except the
unauthenticated probes ` + "`/healthz`, `/readyz`, and `/openapi.yaml`." + `

**Authenticating.** Send one ` + "`Authorization: Bearer <token>`" + ` header. The
token is one of two kinds, and which one you send decides what you can do:

- A **JWT** — a signed login token minted by the frontend for a logged-in person.
  The router verifies the signature against the frontend's public keys (JWKS).
  What the person can do follows their role in the organization: an owner or admin
  can do everything; a member can read and send.
- An **api-key** — a long-lived token for a script or service. It may also be sent
  in an ` + "`x-api-key`" + ` header. Each key is granted some mix of four
  permissions — ` + "`read`, `send`, `manage`, `events`" + ` — and can only call
  endpoints those permissions allow.

Creating users, organizations, and api-keys all happen in the frontend, not here.

**Permissions per endpoint.** Each endpoint requires one capability: ` +
		"`read`" + ` (GET data), ` + "`send`" + ` (send messages, post status, set
presence, run group operations), ` + "`manage`" + ` (create/start/stop sessions,
configure webhooks), or ` + "`events`" + ` (subscribe to realtime events). A JWT
grants these from the person's org role; an api-key grants exactly what it was
given.

**Errors.** Every error response has the same shape:
` + "`{ \"error\": { \"code\": \"...\", \"message\": \"...\", \"details\": ... } }`" + `.
The ` + "`code`" + ` is a stable string (for example ` + "`not_found`, " +
		"`unauthorized`, `validation_error`, `forbidden`, `rate_limited`, " +
		"`not_implemented`, `gateway_unavailable`" + `) and is the field to branch on.

**Pagination.** List endpoints accept ` + "`?limit=`" + ` and an opaque
` + "`?cursor=`" + `, and return ` + "`{ \"data\": [ ... ], \"nextCursor\": \"...\" }`" + `.
Pass the returned ` + "`nextCursor`" + ` back as ` + "`?cursor=`" + ` for the next
page; an absent/empty ` + "`nextCursor`" + ` means there are no more. Treat the
cursor as opaque.

**Realtime & webhooks.** Events are delivered two ways with the identical envelope:
as the body of a webhook ` + "`POST`" + `, and as discrete JSON messages over the
realtime WebSocket on the router. See the **webhooks** section for the typed,
per-event-type payloads.

**Not yet implemented in v1.** Some operations return ` + "`501 not_implemented`" + `:
sending media (image, video, audio, document, sticker), posting an image status,
all channel operations, and approving pending group join requests. Text, poll,
location, and contact message types do work.`)

	o.Servers = []*huma.Server{{URL: "/api/v1", Description: "API base path on the central router"}}

	if o.Components.SecuritySchemes == nil {
		o.Components.SecuritySchemes = map[string]*huma.SecurityScheme{}
	}
	o.Components.SecuritySchemes["bearerAuth"] = &huma.SecurityScheme{
		Type: "http", Scheme: "bearer", BearerFormat: "JWT",
		Description: "Send `Authorization: Bearer <token>`. The router accepts two kinds of token and tries each in turn: a frontend-minted login **JWT** (verified against the frontend JWKS; the person's org + role are read from it), or an **api-key** for a script/service (carrying a fixed set of gateway permissions). The `bearerFormat: JWT` label describes the person-login case.",
	}
	o.Components.SecuritySchemes["apiKeyHeader"] = &huma.SecurityScheme{
		Type: "apiKey", In: "header", Name: "x-api-key",
		Description: "Alternative transport for a better-auth api-key, for clients that cannot set an `Authorization` header. Equivalent to sending the api-key as a Bearer token.",
	}
	o.Security = []map[string][]string{{"bearerAuth": {}}, {"apiKeyHeader": {}}}

	o.Tags = []*huma.Tag{
		{Name: "Sessions", Description: "Attach, pair, start/stop, and manage WhatsApp numbers. A session is one attached number; the create/lifecycle/QR/pairing operations live here (manage capability)."},
		{Name: "Messages", Description: "Send and act on messages: send (text/poll/location/contact; media is 501 in v1), edit, revoke, react, forward, and vote on polls (send capability)."},
		{Name: "Chats", Description: "Read chat history and manage chats: list/get chats and messages (read), mark read, update flags, delete, and set typing presence (send)."},
		{Name: "Contacts", Description: "Look up and manage contacts: list/check/get contacts, profile picture and about (read); block/unblock (send)."},
		{Name: "Groups", Description: "Group lifecycle and membership: list/get/members/invite (read); create, add/remove/promote/demote members, edit, join/leave, and approve requests (send)."},
		{Name: "Channels", Description: "WhatsApp channels (newsletters). All channel operations return 501 not_implemented in v1."},
		{Name: "Status & Presence", Description: "Post status updates and set the session's presence (send). Image status is 501 in v1."},
		{Name: "Webhooks", Description: "Configure webhook endpoints that receive the event envelope over HTTP, with HMAC signing and retries (manage capability)."},
		{Name: "Admin", Description: "Cross-organization oversight for platform super-admins: list all sessions and trigger/inspect history backfills."},
	}
}

// registerEventWebhooks registers each typed event envelope as a component schema
// and adds an OpenAPI 3.1 `webhooks` entry whose request body is a oneOf over them,
// discriminated by the `event` property. This documents exactly what a webhook
// receiver (or realtime WebSocket consumer) will receive, per event type.
func registerEventWebhooks(api huma.API) {
	oapi := api.OpenAPI()
	registry := oapi.Components.Schemas

	var refs []*huma.Schema
	mapping := map[string]string{}
	for _, ev := range apitypes.EventTypeSchemas() {
		t := reflect.TypeOf(ev)
		name := t.Name()
		ref := registry.Schema(t, true, name) // registers the component, returns a $ref
		refs = append(refs, ref)
		// Map each discriminator value (the event type strings) to this schema so
		// tooling can resolve the concrete type from the `event` field.
		for _, val := range eventEnumValues(t) {
			mapping[val] = ref.Ref
		}
	}

	body := &huma.Schema{
		OneOf:         refs,
		Discriminator: &huma.Discriminator{PropertyName: "event", Mapping: mapping},
	}

	if oapi.Webhooks == nil {
		oapi.Webhooks = map[string]*huma.PathItem{}
	}
	oapi.Webhooks["event"] = &huma.PathItem{
		Post: &huma.Operation{
			OperationID: "event",
			Summary:     "Gateway event",
			Description: "The gateway POSTs this body to each configured webhook url when a matching " +
				"event fires, and delivers the identical envelope over the realtime WebSocket as a " +
				"discrete JSON message. The `event` field discriminates which payload shape applies. " +
				"Webhook deliveries carry the event id in the `X-Webhook-Request-Id` header (and an " +
				"HMAC signature in `X-Webhook-Signature` when a secret is configured) so receivers can " +
				"verify and de-duplicate.",
			RequestBody: &huma.RequestBody{
				Required:    true,
				Description: "The event envelope. Exactly one of the listed event shapes, selected by `event`.",
				Content: map[string]*huma.MediaType{
					"application/json": {Schema: body},
				},
			},
			Responses: map[string]*huma.Response{
				"200": {Description: "Return any 2xx to acknowledge receipt. Non-2xx (or a timeout) makes the gateway retry with backoff."},
			},
		},
	}
}

// eventEnumValues reads the `enum:"a,b,c"` tag off the typed event's Event field so
// the discriminator mapping covers every event-type string that envelope represents.
func eventEnumValues(t reflect.Type) []string {
	f, ok := t.FieldByName("Event")
	if !ok {
		return nil
	}
	enum := f.Tag.Get("enum")
	if enum == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(enum); i++ {
		if i == len(enum) || enum[i] == ',' {
			if start < i {
				out = append(out, enum[start:i])
			}
			start = i + 1
		}
	}
	return out
}
