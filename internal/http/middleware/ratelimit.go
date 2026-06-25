package middleware

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// RateLimiter decides whether a request keyed by `key` may proceed. Implemented
// by the outbound redis limiter (or any other backend). Allow returns
// (true, nil) to permit, (false, nil) to deny (-> 429), and a non-nil error on
// backend failure. The middleware fails OPEN on backend errors so a Redis
// outage degrades to no limiting rather than a full outage; the error is logged
// by the limiter implementation.
type RateLimiter interface {
	Allow(ctx context.Context, key string) (bool, error)
}

// RateLimitKeyFunc derives the bucket key for a request. The default
// (SessionOrTenantKey) keys by the :session path param when present, else by the
// resolved tenant — so per-number send limits apply on session routes while
// tenant-wide limits cover the rest.
type RateLimitKeyFunc func(r *http.Request) string

// SessionOrTenantKey keys by the chi :session URL param when it is present on the
// route, otherwise by the context tenant id. This is the documented default: send
// endpoints (which always carry :session) are limited per WhatsApp number, while
// other endpoints fall back to a tenant-wide bucket.
func SessionOrTenantKey(r *http.Request) string {
	if s := chi.URLParam(r, "session"); s != "" {
		return "session:" + s
	}
	if t := httpx.TenantID(r.Context()); t != "" {
		return "tenant:" + t
	}
	return "anon"
}

// RateLimit enforces `limiter` using keyFn (defaults to SessionOrTenantKey when
// nil). On deny it writes a 429 envelope; on a limiter backend error it fails
// open (allows the request).
func RateLimit(limiter RateLimiter, keyFn RateLimitKeyFunc) func(http.Handler) http.Handler {
	if keyFn == nil {
		keyFn = SessionOrTenantKey
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, err := limiter.Allow(r.Context(), keyFn(r))
			if err == nil && !allowed {
				httpx.WriteError(w, domain.ErrRateLimited("rate limit exceeded"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
