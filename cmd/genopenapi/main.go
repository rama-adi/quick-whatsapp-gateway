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
