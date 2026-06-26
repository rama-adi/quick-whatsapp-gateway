package authz

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// countingVerifier is a KeyVerifier that records how many times it was called
// and returns a canned Principal (or error) per raw key. It lets the cache tests
// assert hit/miss by counting delegated calls.
type countingVerifier struct {
	mu        sync.Mutex
	calls     int
	byRaw     map[string]*Principal
	failErr   error
	failOnRaw string
}

func (v *countingVerifier) VerifyKey(_ context.Context, raw string) (*Principal, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	if v.failErr != nil && (v.failOnRaw == "" || v.failOnRaw == raw) {
		return nil, v.failErr
	}
	p, ok := v.byRaw[raw]
	if !ok {
		return nil, errors.New("authz: unknown key")
	}
	cp := *p
	return &cp, nil
}

func (v *countingVerifier) callCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

// manualClock is a controllable clock for TTL tests.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func principalFor(keyID, orgID, userID string) *Principal {
	return &Principal{
		Kind:           KindAPIKey,
		OrganizationID: orgID,
		UserID:         userID,
		KeyID:          keyID,
	}
}

func TestCachingKeyVerifier_HitMiss(t *testing.T) {
	clk := &manualClock{now: time.UnixMilli(1_700_000_000_000)}
	inner := &countingVerifier{byRaw: map[string]*Principal{
		"raw_a": principalFor("key_a", "org_a", ""),
	}}
	c := NewCachingKeyVerifier(inner, DefaultKeyCacheTTL, WithCacheClock(clk.Now))

	// First call is a miss → delegates.
	if _, err := c.VerifyKey(context.Background(), "raw_a"); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if got := inner.callCount(); got != 1 {
		t.Fatalf("after first call, delegate calls = %d, want 1", got)
	}

	// Second call within TTL is a hit → no further delegation.
	if _, err := c.VerifyKey(context.Background(), "raw_a"); err != nil {
		t.Fatalf("second verify: %v", err)
	}
	if got := inner.callCount(); got != 1 {
		t.Fatalf("after cached hit, delegate calls = %d, want 1 (hit should not delegate)", got)
	}
}

func TestCachingKeyVerifier_ErrorsNotCached(t *testing.T) {
	clk := &manualClock{now: time.UnixMilli(1_700_000_000_000)}
	inner := &countingVerifier{
		byRaw:   map[string]*Principal{},
		failErr: errors.New("authz: revoked"),
	}
	c := NewCachingKeyVerifier(inner, DefaultKeyCacheTTL, WithCacheClock(clk.Now))

	// Fail-closed: every failing verify re-delegates (never memoized).
	for i := 0; i < 3; i++ {
		if _, err := c.VerifyKey(context.Background(), "raw_bad"); err == nil {
			t.Fatalf("expected error on attempt %d", i)
		}
	}
	if got := inner.callCount(); got != 3 {
		t.Fatalf("failing verifies delegated %d times, want 3 (errors must not be cached)", got)
	}
}

func TestCachingKeyVerifier_TTLExpiry(t *testing.T) {
	clk := &manualClock{now: time.UnixMilli(1_700_000_000_000)}
	inner := &countingVerifier{byRaw: map[string]*Principal{
		"raw_a": principalFor("key_a", "org_a", ""),
	}}
	c := NewCachingKeyVerifier(inner, 60*time.Second, WithCacheClock(clk.Now))

	if _, err := c.VerifyKey(context.Background(), "raw_a"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Just before expiry: still a hit.
	clk.advance(59 * time.Second)
	if _, err := c.VerifyKey(context.Background(), "raw_a"); err != nil {
		t.Fatalf("verify pre-expiry: %v", err)
	}
	if got := inner.callCount(); got != 1 {
		t.Fatalf("pre-expiry delegate calls = %d, want 1", got)
	}
	// At/after expiry: miss → re-delegate.
	clk.advance(time.Second)
	if _, err := c.VerifyKey(context.Background(), "raw_a"); err != nil {
		t.Fatalf("verify post-expiry: %v", err)
	}
	if got := inner.callCount(); got != 2 {
		t.Fatalf("post-expiry delegate calls = %d, want 2 (entry should have expired)", got)
	}
}

func TestCachingKeyVerifier_Eviction(t *testing.T) {
	tests := []struct {
		name string
		// seed maps raw -> principal; all are verified (cached) before eviction.
		seed map[string]*Principal
		// evict runs the eviction under test.
		evict func(c *CachingKeyVerifier)
		// wantMissAfter lists raw keys that should re-delegate after eviction.
		wantMissAfter []string
		// wantHitAfter lists raw keys that should still be cached after eviction.
		wantHitAfter []string
	}{
		{
			name: "evict by keyId",
			seed: map[string]*Principal{
				"raw_a": principalFor("key_a", "org_1", "user_1"),
				"raw_b": principalFor("key_b", "org_1", "user_2"),
			},
			evict:         func(c *CachingKeyVerifier) { c.EvictKey("key_a") },
			wantMissAfter: []string{"raw_a"},
			wantHitAfter:  []string{"raw_b"},
		},
		{
			name: "evict by userId",
			seed: map[string]*Principal{
				"raw_a": principalFor("key_a", "org_1", "user_1"),
				"raw_b": principalFor("key_b", "org_2", "user_1"),
				"raw_c": principalFor("key_c", "org_1", "user_2"),
			},
			evict:         func(c *CachingKeyVerifier) { c.EvictUser("user_1") },
			wantMissAfter: []string{"raw_a", "raw_b"},
			wantHitAfter:  []string{"raw_c"},
		},
		{
			name: "evict by org",
			seed: map[string]*Principal{
				"raw_a": principalFor("key_a", "org_1", "user_1"),
				"raw_b": principalFor("key_b", "org_2", "user_2"),
			},
			evict:         func(c *CachingKeyVerifier) { c.EvictOrg("org_1") },
			wantMissAfter: []string{"raw_a"},
			wantHitAfter:  []string{"raw_b"},
		},
		{
			name: "evict by user+org",
			seed: map[string]*Principal{
				"raw_a": principalFor("key_a", "org_1", "user_1"),
				"raw_b": principalFor("key_b", "org_2", "user_1"),
			},
			evict:         func(c *CachingKeyVerifier) { c.EvictUserOrg("user_1", "org_1") },
			wantMissAfter: []string{"raw_a"},
			wantHitAfter:  []string{"raw_b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := &manualClock{now: time.UnixMilli(1_700_000_000_000)}
			inner := &countingVerifier{byRaw: tt.seed}
			c := NewCachingKeyVerifier(inner, DefaultKeyCacheTTL, WithCacheClock(clk.Now))

			// Warm the cache for every seeded key.
			for raw := range tt.seed {
				if _, err := c.VerifyKey(context.Background(), raw); err != nil {
					t.Fatalf("warm %q: %v", raw, err)
				}
			}
			before := inner.callCount()
			if before != len(tt.seed) {
				t.Fatalf("warm-up delegated %d times, want %d", before, len(tt.seed))
			}

			tt.evict(c)

			// Evicted keys re-delegate; surviving keys stay cached.
			for _, raw := range tt.wantHitAfter {
				if _, err := c.VerifyKey(context.Background(), raw); err != nil {
					t.Fatalf("hit %q: %v", raw, err)
				}
			}
			if got := inner.callCount(); got != before {
				t.Fatalf("expected no new delegations for surviving keys, got %d (was %d)", got, before)
			}
			for _, raw := range tt.wantMissAfter {
				if _, err := c.VerifyKey(context.Background(), raw); err != nil {
					t.Fatalf("miss %q: %v", raw, err)
				}
			}
			if got := inner.callCount(); got != before+len(tt.wantMissAfter) {
				t.Fatalf("evicted keys delegated %d total, want %d", got, before+len(tt.wantMissAfter))
			}
		})
	}
}

func TestCachingKeyVerifier_EmptyKeyDelegates(t *testing.T) {
	inner := &countingVerifier{byRaw: map[string]*Principal{}}
	c := NewCachingKeyVerifier(inner, DefaultKeyCacheTTL)
	if _, err := c.VerifyKey(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty key")
	}
	if got := inner.callCount(); got != 1 {
		t.Fatalf("empty key delegate calls = %d, want 1", got)
	}
}
