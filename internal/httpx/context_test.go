package httpx

import (
	"context"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

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
