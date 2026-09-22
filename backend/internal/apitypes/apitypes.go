// Package apitypes holds the shared API DTOs that are the source of truth for the
// generated OpenAPI spec (docs/plans/plan-router-impl.md D11). The gateway defines
// the resource request/response shapes here (and in the per-resource huma
// operation files); huma generates docs/openapi.yaml from them via cmd/genopenapi.
package apitypes

// List is the cursor-paginated list envelope (§11): {"data":[...],"nextCursor"?}.
// It is the huma response Body for every list operation, replacing the old
// hand-rolled httpx.ListEnvelope so the wire shape is declared in one typed place.
type List[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// NewList builds a List, normalizing a nil slice to an empty one so clients always
// see a JSON array rather than null.
func NewList[T any](items []T, nextCursor string) List[T] {
	if items == nil {
		items = []T{}
	}
	return List[T]{Data: items, NextCursor: nextCursor}
}
