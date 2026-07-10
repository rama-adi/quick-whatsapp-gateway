package assertion

import (
	"sync"
	"time"
)

// NonceCache is the concurrency-safe, in-memory anti-replay store for assertion
// JWT IDs. SeenBefore atomically checks and records a nonce until token expiry, so
// simultaneous redemptions cannot both succeed. Expired entries are swept at a
// bounded cadence on the request path rather than by a background goroutine.
//
// The cache protects only one gateway process. Deployments that load-balance the
// same gateway identity across multiple replicas must replace it with a shared
// implementation; otherwise the same assertion could be redeemed once per
// replica.
type NonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time // jti -> expiry
	now  func() time.Time
	last time.Time // last sweep time, to amortize GC
}

// NewNonceCache builds an empty in-memory nonce cache.
func NewNonceCache() *NonceCache {
	return &NonceCache{seen: make(map[string]time.Time), now: time.Now}
}

// SeenBefore records jti with the given expiry and reports whether it was already
// present (i.e. a replay). A zero/empty jti is treated as always-replay (rejected)
// because an unbound nonce provides no replay protection.
func (c *NonceCache) SeenBefore(jti string, expiry time.Time) bool {
	if jti == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.sweepLocked(now)
	if exp, ok := c.seen[jti]; ok && exp.After(now) {
		return true
	}
	c.seen[jti] = expiry
	return false
}

// sweepLocked evicts expired entries at most once per second so a long-lived
// gateway doesn't accumulate dead nonces. Caller holds c.mu.
func (c *NonceCache) sweepLocked(now time.Time) {
	if now.Sub(c.last) < time.Second {
		return
	}
	c.last = now
	for k, exp := range c.seen {
		if !exp.After(now) {
			delete(c.seen, k)
		}
	}
}
