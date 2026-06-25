package middleware

import (
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// CookieSession bridges Authula's cookie auth into this app's request context so
// dashboard (browser) requests carry a tenant id the same way API-key requests
// do. It composes two collaborators supplied by the auth package:
//
//   - optionalAuth: Authula's OPTIONAL cookie middleware (populates the actor in
//     the request when a session cookie is present, never 401s). Pass
//     Auth.OptionalCookieAuth().
//   - tenantFrom:   resolves the tenant id from a request once optionalAuth has
//     run (returns ok=false when unauthenticated). Pass Auth.CurrentTenantID.
//
// After optionalAuth runs, if a tenant is resolved it is stored via
// httpx.SetTenantID. This middleware NEVER rejects — it only enriches context;
// gating is the caller's job (e.g. RequireDashboardAuth or an Authula route
// mapping). It is the cookie-side counterpart to APIKeyAuth.
func CookieSession(
	optionalAuth func(http.Handler) http.Handler,
	tenantFrom func(*http.Request) (string, bool),
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// inner runs after optionalAuth has populated the actor; it lifts the
		// tenant id into our context for downstream handlers/logging.
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id, ok := tenantFrom(r); ok && id != "" {
				r = r.WithContext(httpx.SetTenantID(r.Context(), id))
			}
			next.ServeHTTP(w, r)
		})
		return optionalAuth(inner)
	}
}

// RequireTenant rejects with 401 when no tenant id is present in the context. Use
// it after CookieSession (or APIKeyAuth) on routes that accept EITHER credential:
// compose the two enrichers, then gate once with RequireTenant.
func RequireTenant() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if httpx.TenantID(r.Context()) == "" {
				httpx.WriteError(w, domain.ErrUnauthorized("authentication required"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
