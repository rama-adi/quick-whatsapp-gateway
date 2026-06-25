package httpx

import (
	"net/http"
	"strconv"
)

// Pagination defaults and bounds for ?limit=&cursor= lists (masterplan §11).
const (
	DefaultLimit = 50
	MaxLimit     = 200
	MinLimit     = 1
)

// ParsePage reads ?limit= and ?cursor= from the request. limit is clamped to
// [MinLimit, MaxLimit] and defaults to DefaultLimit when absent or unparseable;
// cursor is returned verbatim as the opaque page token ("" when absent). The
// repos own the cursor's meaning — this only carries it.
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

// listBody is the §11 list envelope: {"data":[...],"nextCursor":...}. nextCursor
// is omitted when empty (no further pages).
type listBody[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// ListEnvelope writes a 200 list response wrapping items in {"data":...} with the
// opaque nextCursor. A nil items slice is normalized to [] so clients always see
// an array.
func ListEnvelope[T any](w http.ResponseWriter, items []T, nextCursor string) {
	if items == nil {
		items = []T{}
	}
	WriteJSON(w, http.StatusOK, listBody[T]{Data: items, NextCursor: nextCursor})
}
