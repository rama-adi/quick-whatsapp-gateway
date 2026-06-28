package assertion

import (
	"sync"
	"time"
)

// NonceCache is the anti-replay store: it remembers each assertion's jti until it
// expires, so a captured assertion cannot be redeemed twice within its freshness
// window. In-memory per gateway instance is sufficient unless a single gateway
// runs multiple replicas behind a load balancer, in which case the nonce store
// must be shared (D3) — swap this for a Redis-backed implementation of the same
// SeenBefore contract.
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
