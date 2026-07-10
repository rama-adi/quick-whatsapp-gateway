package httpx

import (
	"context"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// TestOrganizationIDCtx starts with a bare context, then stores and reads an organization ID.
// It expects the zero value before insertion and the exact tenant identifier afterward.
// This protects organization-scoped handlers from context-key collisions or accidental fallback tenants.
func TestOrganizationIDCtx(t *testing.T) {
	ctx := context.Background()
	if OrganizationID(ctx) != "" {
		t.Fatal("empty ctx should yield empty organization")
	}
	ctx = SetOrganizationID(ctx, "tnt_123")
	if got := OrganizationID(ctx); got != "tnt_123" {
		t.Fatalf("OrganizationID = %q", got)
	}
}

// TestAPIKeyCtx stores a verified API-key pointer and retrieves it through the shared accessor.
// It expects nil when authentication did not set a key and preserves the authenticated key identity when present.
// This guards audit and revocation code that distinguishes API-key callers from human principals.
func TestAPIKeyCtx(t *testing.T) {
	ctx := context.Background()
	if APIKeyCtx(ctx) != nil {
		t.Fatal("empty ctx should yield nil key")
	}
	key := &domain.APIKey{ID: "key_1", OrganizationID: "tnt_123"}
	ctx = SetAPIKey(ctx, key)
	if got := APIKeyCtx(ctx); got == nil || got.ID != "key_1" {
		t.Fatalf("APIKeyCtx = %+v", got)
	}
}

// TestRequestIDCtx exercises the request-correlation context slot from empty context through insertion.
// It expects no invented value and exact propagation of the middleware-generated ID.
// This prevents unrelated context values from contaminating logs and response correlation.
func TestRequestIDCtx(t *testing.T) {
	ctx := context.Background()
	if RequestID(ctx) != "" {
		t.Fatal("empty ctx should yield empty request id")
	}
	ctx = SetRequestID(ctx, "req_abc")
	if got := RequestID(ctx); got != "req_abc" {
		t.Fatalf("RequestID = %q", got)
	}
}
