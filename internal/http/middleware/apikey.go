package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// APIKeyVerifier resolves a presented raw bearer key to its API key and the owning
// organization id (§4.2). Implemented by the read-only key verifier service; the
// middleware depends only on this interface. A nil error with a non-nil key and a
// non-empty org id means the key is valid; return a *domain.APIError (unauthorized)
// to reject. Any other error is treated as a 401 too (the middleware never leaks
// internal detail on the auth path) but is logged by the verifier as appropriate.
type APIKeyVerifier interface {
	Verify(ctx context.Context, rawKey string) (*domain.APIKey, string, error)
}

// APIKeyAuth authenticates requests via "Authorization: Bearer <key>". On success
// it stores the API key and organization id in the context (httpx.SetAPIKey /
// httpx.SetOrganizationID) and calls next. On a missing/malformed header or a
// verifier rejection it writes a 401 envelope and stops.
func APIKeyAuth(verifier APIKeyVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				httpx.WriteError(w, domain.ErrUnauthorized("missing or malformed Authorization header"))
				return
			}
			key, orgID, err := verifier.Verify(r.Context(), raw)
			if err != nil || key == nil || orgID == "" {
				httpx.WriteError(w, domain.ErrUnauthorized("invalid API key"))
				return
			}
			ctx := httpx.SetAPIKey(r.Context(), key)
			ctx = httpx.SetOrganizationID(ctx, orgID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerToken extracts the credential from an "Authorization: Bearer <token>"
// header (scheme match is case-insensitive). The bool reports success.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

// requirePermission returns middleware that 403s unless the context-resolved API
// key grants the permission selected by `has`. It 401s when no API key is present
// (auth must run first). Cookie-session (dashboard) requests carry no API key and
// are not gated by these — they use Authula RBAC.
func requirePermission(name string, has func(domain.Permissions) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := httpx.APIKeyCtx(r.Context())
			if key == nil {
				httpx.WriteError(w, domain.ErrUnauthorized("authentication required"))
				return
			}
			if !has(key.Permissions) {
				httpx.WriteError(w, domain.ErrForbidden("missing required permission: "+name))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireRead gates a route on the API key's read permission.
func RequireRead() func(http.Handler) http.Handler {
	return requirePermission("read", func(p domain.Permissions) bool { return p.Read })
}

// RequireSend gates a route on the API key's send permission.
func RequireSend() func(http.Handler) http.Handler {
	return requirePermission("send", func(p domain.Permissions) bool { return p.Send })
}

// RequireManage gates a route on the API key's manage permission.
func RequireManage() func(http.Handler) http.Handler {
	return requirePermission("manage", func(p domain.Permissions) bool { return p.Manage })
}

// RequireEvents gates a route on the API key's events permission (the NDJSON
// stream).
func RequireEvents() func(http.Handler) http.Handler {
	return requirePermission("events", func(p domain.Permissions) bool { return p.Events })
}
