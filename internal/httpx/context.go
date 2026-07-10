package httpx

import (
	"context"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// ctxKey is private so values cannot collide with keys from middleware or third
// parties. These slots carry request-scoped identity and correlation data only;
// they must not be used for optional dependencies, mutable global state, or
// values whose lifetime extends beyond the request.
type ctxKey int

const (
	ctxKeyOrganizationID ctxKey = iota
	ctxKeyAPIKey
	ctxKeyRequestID
	ctxKeyPrincipal
)

// SetOrganizationID returns a child context carrying the already verified
// better-auth organization ID. Authentication or assertion middleware is the
// writer; handlers treat the value as an isolation key but must still handle an
// empty read as unauthenticated rather than inventing a default organization.
func SetOrganizationID(ctx context.Context, organizationID string) context.Context {
	return context.WithValue(ctx, ctxKeyOrganizationID, organizationID)
}

// OrganizationID returns the request's verified organization ID, or "" when the
// context lacks the correctly typed value. It performs no validation or fallback.
func OrganizationID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyOrganizationID).(string); ok {
		return v
	}
	return ""
}

// SetAPIKey returns a child context carrying the authenticated API-key snapshot.
// The pointer is not copied: callers must treat the domain.APIKey as immutable for
// the remainder of the request. Human-authenticated requests leave this slot nil.
func SetAPIKey(ctx context.Context, key *domain.APIKey) context.Context {
	return context.WithValue(ctx, ctxKeyAPIKey, key)
}

// APIKeyCtx returns the authenticated API-key snapshot or nil when absent or of
// the wrong type. A nil result distinguishes non-key callers; it is not proof that
// the overall request is unauthenticated.
func APIKeyCtx(ctx context.Context) *domain.APIKey {
	if v, ok := ctx.Value(ctxKeyAPIKey).(*domain.APIKey); ok {
		return v
	}
	return nil
}

// SetPrincipalValue stores the verified caller as an opaque immutable value. The
// authz package is the only production writer and supplies *authz.Principal;
// opacity here breaks an import cycle while preserving one collision-free slot.
// Consumers should use authz.FromContext rather than asserting this value
// directly.
func SetPrincipalValue(ctx context.Context, p any) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal, p)
}

// PrincipalValue returns the opaque principal value or nil. It exists for the
// authz typed adapter and does not itself establish that the value was verified.
func PrincipalValue(ctx context.Context) any {
	return ctx.Value(ctxKeyPrincipal)
}

// SetRequestID returns a child context carrying the sanitized correlation ID.
// RequestID middleware is responsible for bounding and validating external input
// before calling it; this transport helper stores the string verbatim.
func SetRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// RequestID returns the request correlation ID or "" when absent or mistyped.
// Loggers may use the empty value but must not synthesize a second ID downstream.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}
