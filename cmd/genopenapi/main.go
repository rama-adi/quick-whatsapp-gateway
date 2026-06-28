// Command genopenapi generates the OpenAPI contract from the shared Go types
// (docs/plans/plan-router-impl.md D11): it builds one huma API from every resource
// registrar (without running a server), serializes the spec to YAML, and writes it
// to the output path. `make openapi` runs this; CI runs `make openapi && git diff
// --exit-code` so the committed spec can never drift from the Go types.
//
// During the resource-by-resource conversion this writes a side file
// (docs/openapi.gen.yaml) so the hand-written docs/openapi.yaml the router serves
// stays intact until every endpoint is converted; the final cutover swaps the
// generated file in and deletes the hand-written source.
package main

import (
	"fmt"
	"os"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

func main() {
	out := "docs/openapi.gen.yaml"
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
