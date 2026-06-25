package auth

import (
	"context"
	"fmt"
	"strings"
)

// TenantSyncer mirrors Authula users into the app-side tenants table (§5). It is
// the hook Phase 3 calls after sign-up (and may call on login) to keep the mirror
// fresh. It holds only consumer-defined collaborators so it is fully testable.
type TenantSyncer struct {
	tenants TenantStore
	clock   Clock
}

// NewTenantSyncer constructs a TenantSyncer. A nil clock falls back to wall time.
func NewTenantSyncer(tenants TenantStore, clock Clock) *TenantSyncer {
	if clock == nil {
		clock = wallClock{}
	}
	return &TenantSyncer{tenants: tenants, clock: clock}
}

// SyncTenant upserts the tenants row for an Authula user, keyed by the Authula
// user id (= tenants.id, §5). Idempotent: safe to call on every sign-up/login.
func (s *TenantSyncer) SyncTenant(ctx context.Context, userID, email string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("auth: SyncTenant: empty userID")
	}
	if err := s.tenants.UpsertTenant(ctx, userID, strings.TrimSpace(email), s.clock.NowMs()); err != nil {
		return fmt.Errorf("auth: SyncTenant upsert %q: %w", userID, err)
	}
	return nil
}
