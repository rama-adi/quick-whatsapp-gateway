package httpx

import (
	"context"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// ctxKey is an unexported type so these context keys can't collide with keys set
// by other packages. The actual setters used in production live in middleware;
// the getters here are the read side every handler/service shares.
type ctxKey int

const (
	ctxKeyTenantID ctxKey = iota
	ctxKeyAPIKey
	ctxKeyRequestID
)

// SetTenantID returns a child context carrying the resolved tenant id. Set by the
// auth middleware (API key or cookie session).
func SetTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, ctxKeyTenantID, tenantID)
}

// TenantID returns the request's tenant id, or "" if none was set.
func TenantID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyTenantID).(string); ok {
		return v
	}
	return ""
}

// SetAPIKey returns a child context carrying the authenticated API key. Set by
// the API-key auth middleware; nil for cookie-authenticated dashboard requests.
func SetAPIKey(ctx context.Context, key *domain.APIKey) context.Context {
	return context.WithValue(ctx, ctxKeyAPIKey, key)
}

// APIKeyCtx returns the authenticated API key, or nil if the request was not
// authenticated by an API key (e.g. a cookie session).
func APIKeyCtx(ctx context.Context) *domain.APIKey {
	if v, ok := ctx.Value(ctxKeyAPIKey).(*domain.APIKey); ok {
		return v
	}
	return nil
}

// SetRequestID returns a child context carrying the request id. Set by the
// RequestID middleware.
func SetRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// RequestID returns the request's id, or "" if none was set.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}
