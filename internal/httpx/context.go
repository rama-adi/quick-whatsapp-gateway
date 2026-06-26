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
	ctxKeyOrganizationID ctxKey = iota
	ctxKeyAPIKey
	ctxKeyRequestID
	ctxKeyPrincipal
)

// SetOrganizationID returns a child context carrying the resolved organization id
// (a better-auth organization id). Set by the auth middleware (JWT or api-key).
func SetOrganizationID(ctx context.Context, organizationID string) context.Context {
	return context.WithValue(ctx, ctxKeyOrganizationID, organizationID)
}

// OrganizationID returns the request's organization id, or "" if none was set.
func OrganizationID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyOrganizationID).(string); ok {
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

// SetPrincipalValue stores the verified caller on the context as an opaque
// value. The authz package wraps this with a typed SetPrincipal/Principal pair
// (the value is an *authz.Principal); keeping the slot untyped here avoids an
// import cycle between httpx and authz while still sharing one context key.
func SetPrincipalValue(ctx context.Context, p any) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal, p)
}

// PrincipalValue returns the opaque principal value, or nil if none was set. Use
// authz.Principal(ctx) for the typed accessor.
func PrincipalValue(ctx context.Context) any {
	return ctx.Value(ctxKeyPrincipal)
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
