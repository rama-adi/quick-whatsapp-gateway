package middleware

import (
	"context"
	"net/http"
	"time"
)

// DefaultRequestTimeout bounds how long any single API request may run before its
// context is cancelled. It is the backstop that turns a wedged downstream call (a
// stalled MySQL query, an exhausted connection pool with no free checkout, a
// contended row) into a prompt 5xx instead of an indefinite hang: the deadline
// propagates through r.Context() into every ctx-aware DB query, so the query is
// cancelled and the handler returns an error rather than blocking forever.
//
// Chosen well above the observed idle p100 (~240ms) yet low enough that a client
// with its own short abort never has to be the first line of defense.
const DefaultRequestTimeout = 15 * time.Second

// Timeout attaches a deadline to every request's context. When d <= 0 it is a
// no-op passthrough (used by tests). The deadline is the single source of a
// bounded request lifetime: handlers thread r.Context() into store queries, so an
// expired deadline surfaces as context.DeadlineExceeded from the query and maps
// to a 503 at the huma/httpx edge — never a silent hang.
//
// This is deliberately a context deadline rather than http.TimeoutHandler: the
// latter abandons the goroutine (leaking the wedged DB call and its pooled
// connection) whereas cancelling the context unwinds the query and releases the
// connection back to the pool.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
