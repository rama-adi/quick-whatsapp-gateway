package stream

import "strings"

// eventFilter decides whether an event type should be delivered on a connection,
// per the events= query param (§9): "*" (or empty) means all types, otherwise a
// comma-separated allow-list of exact type names.
type eventFilter struct {
	all   bool                // true => deliver everything
	types map[string]struct{} // explicit allow-list (when !all)
}

// parseEventFilter parses the raw events= value. The empty string and "*"
// (anywhere in the list) both mean "all". Otherwise each comma-separated token is
// trimmed; blank tokens are ignored. A filter with no usable tokens and no "*"
// matches nothing — callers should treat that as a client error before use, but
// the zero-value behaviour is safe (matches nothing).
func parseEventFilter(raw string) eventFilter {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" {
		return eventFilter{all: true}
	}
	f := eventFilter{types: make(map[string]struct{})}
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if tok == "*" {
			return eventFilter{all: true}
		}
		f.types[tok] = struct{}{}
	}
	return f
}

// allows reports whether an event of the given type passes the filter.
func (f eventFilter) allows(eventType string) bool {
	if f.all {
		return true
	}
	_, ok := f.types[eventType]
	return ok
}

// empty reports whether the filter would match nothing (no "*" and no types).
// Used to reject a request like ?events= (resolved to nothing meaningful) with a
// validation error instead of opening a stream that never emits.
func (f eventFilter) empty() bool {
	return !f.all && len(f.types) == 0
}
