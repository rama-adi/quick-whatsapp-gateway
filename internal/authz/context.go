package authz

import (
	"context"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// SetPrincipal returns a child context carrying the verified caller. The auth
// middleware sets it; handlers read it via Principal. It also mirrors the
// principal's organization id into httpx.SetOrganizationID so existing
// org-scoped handlers/logging keep working unchanged (§4.3, Stage-2 contract).
func SetPrincipal(ctx context.Context, p *Principal) context.Context {
	ctx = httpx.SetPrincipalValue(ctx, p)
	if p != nil {
		ctx = httpx.SetOrganizationID(ctx, p.OrganizationID)
	}
	return ctx
}

// FromContext returns the verified caller on the context, or nil if the request
// was not authenticated.
func FromContext(ctx context.Context) *Principal {
	if p, ok := httpx.PrincipalValue(ctx).(*Principal); ok {
		return p
	}
	return nil
}
