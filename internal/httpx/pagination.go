package httpx

import (
	"net/http"
	"strconv"
)

// Pagination defaults and bounds cap repository work for ?limit=&cursor= list
// operations (masterplan §11). Values outside the interval are clamped rather
// than rejected so clients receive a predictable page while the server retains a
// hard upper bound.
const (
	DefaultLimit = 50
	MaxLimit     = 200
	MinLimit     = 1
)

// ParsePage reads limit and cursor query parameters without interpreting
// repository cursor contents. Missing or malformed limits use DefaultLimit;
// numeric values are clamped to [MinLimit, MaxLimit]. The cursor is returned
// verbatim, including malformed tokens, because the owning repository defines
// its encoding and error semantics.
func ParsePage(r *http.Request) (limit int, cursor string) {
	limit = DefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	if limit < MinLimit {
		limit = MinLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	cursor = r.URL.Query().Get("cursor")
	return limit, cursor
}

// listBody is the internal §11 wire envelope. Data is always serialized as an
// array by ListEnvelope, and nextCursor is omitted when the repository signals a
// terminal page with an empty string.
type listBody[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// ListEnvelope writes a 200 response containing items and an opaque continuation
// cursor. It normalizes a nil Go slice to an empty non-nil slice so JSON clients
// always receive data:[] rather than data:null. Cursor omission is controlled
// only by the empty string and does not inspect or transform repository tokens.
func ListEnvelope[T any](w http.ResponseWriter, items []T, nextCursor string) {
	if items == nil {
		items = []T{}
	}
	WriteJSON(w, http.StatusOK, listBody[T]{Data: items, NextCursor: nextCursor})
}
