package auth

import (
	"context"
	"errors"
	"testing"
)

// TestSyncTenantUpserts verifies SyncTenant forwards id/email/now to the store.
func TestSyncTenantUpserts(t *testing.T) {
	store := &fakeTenantStore{}
	s := NewTenantSyncer(store, fixedClock{ms: 1719400000000})

	if err := s.SyncTenant(context.Background(), "user_1", "a@example.com"); err != nil {
		t.Fatalf("SyncTenant: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("UpsertTenant calls = %d, want 1", store.calls)
	}
	if store.lastID != "user_1" || store.lastEml != "a@example.com" || store.lastNow != 1719400000000 {
		t.Fatalf("unexpected upsert args: id=%q email=%q now=%d", store.lastID, store.lastEml, store.lastNow)
	}
}

// TestSyncTenantTrimsAndValidates checks whitespace handling + empty-id guard.
func TestSyncTenantTrimsAndValidates(t *testing.T) {
	store := &fakeTenantStore{}
	s := NewTenantSyncer(store, fixedClock{ms: 1})

	if err := s.SyncTenant(context.Background(), "  user_2 ", "  b@example.com "); err != nil {
		t.Fatalf("SyncTenant: %v", err)
	}
	if store.lastID != "user_2" || store.lastEml != "b@example.com" {
		t.Fatalf("expected trimmed args, got id=%q email=%q", store.lastID, store.lastEml)
	}

	if err := s.SyncTenant(context.Background(), "   ", "x@example.com"); err == nil {
		t.Fatal("expected error on empty userID")
	}
}

// TestSyncTenantPropagatesStoreError wraps the store error.
func TestSyncTenantPropagatesStoreError(t *testing.T) {
	store := &fakeTenantStore{err: errors.New("write failed")}
	s := NewTenantSyncer(store, fixedClock{ms: 1})

	if err := s.SyncTenant(context.Background(), "user_3", "c@example.com"); err == nil {
		t.Fatal("expected store error to propagate")
	}
}

// TestNewTenantSyncerDefaultClock ensures a nil clock falls back to wall time.
func TestNewTenantSyncerDefaultClock(t *testing.T) {
	store := &fakeTenantStore{}
	s := NewTenantSyncer(store, nil)
	if err := s.SyncTenant(context.Background(), "user_4", "d@example.com"); err != nil {
		t.Fatalf("SyncTenant: %v", err)
	}
	if store.lastNow <= 0 {
		t.Fatalf("expected wall-clock timestamp, got %d", store.lastNow)
	}
}
