package authz

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"
)

// CachingKeyVerifier is a positive cache in front of a KeyVerifier (§4.6). A
// busy client should not cost a MySQL lookup per request, and a brief DB blip
// shouldn't drop in-flight callers — so a successfully-verified key is cached
// for a short TTL (~60s). The cache is FAIL-CLOSED: only successes are cached
// (an error is never memoized), and the short TTL is the backstop — even if a
// gateway misses a control-bus revocation, the entry ages out within the window
// and the next refresh fails closed against the (now gone) `apikey` row.
//
// The cache is keyed by the SHA-256 of the raw key (never the raw key itself)
// and is also indexed by keyId, userId and organizationId so the control bus can
// evict precisely on apikey.revoked / user.banned / member.removed.
//
// *CachingKeyVerifier itself satisfies KeyVerifier, so it drops into the auth
// middleware in place of the bare verifier.
type CachingKeyVerifier struct {
	inner KeyVerifier
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	entries map[string]*cacheEntry // keyed by raw-key hash
}

// cacheEntry is one validated api-key held in the positive cache. It snapshots
// the Principal fields plus the resolution identity used for eviction.
type cacheEntry struct {
	principal Principal
	keyID     string
	orgID     string
	userID    string
	expiresAt time.Time // cache expiry (now + ttl), NOT the apikey's own expiry
}

// CacheOption configures a CachingKeyVerifier.
type CacheOption func(*CachingKeyVerifier)

// WithCacheClock injects the clock used for TTL math. Tests pass a controllable
// clock; production leaves it at time.Now.
func WithCacheClock(now func() time.Time) CacheOption {
	return func(c *CachingKeyVerifier) {
		if now != nil {
			c.now = now
		}
	}
}

// DefaultKeyCacheTTL is the positive-cache window (§4.6): the revocation backstop.
const DefaultKeyCacheTTL = 60 * time.Second

// NewCachingKeyVerifier wraps inner with a positive cache. A non-positive ttl
// uses DefaultKeyCacheTTL.
func NewCachingKeyVerifier(inner KeyVerifier, ttl time.Duration, opts ...CacheOption) *CachingKeyVerifier {
	if ttl <= 0 {
		ttl = DefaultKeyCacheTTL
	}
	c := &CachingKeyVerifier{
		inner:   inner,
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[string]*cacheEntry),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// VerifyKey implements KeyVerifier: serve from cache on a live hit, else delegate
// to the inner verifier and cache a success. Errors are never cached (fail-closed).
func (c *CachingKeyVerifier) VerifyKey(ctx context.Context, raw string) (*Principal, error) {
	if raw == "" {
		// Delegate so the inner verifier owns the canonical error.
		return c.inner.VerifyKey(ctx, raw)
	}
	h := cacheKeyHash(raw)

	if p, ok := c.lookup(h); ok {
		return p, nil
	}

	p, err := c.inner.VerifyKey(ctx, raw)
	if err != nil {
		return nil, err
	}
	c.store(h, p)
	// Return a copy so a caller mutating the Principal can't corrupt the cache.
	cp := *p
	return &cp, nil
}

// lookup returns a cached Principal for the key hash if present and unexpired.
func (c *CachingKeyVerifier) lookup(h string) (*Principal, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[h]
	if !ok {
		return nil, false
	}
	if !c.now().Before(e.expiresAt) {
		// Expired: drop it so the map doesn't grow unbounded with stale entries.
		delete(c.entries, h)
		return nil, false
	}
	cp := e.principal
	return &cp, true
}

// store inserts a freshly-verified Principal under the key hash.
func (c *CachingKeyVerifier) store(h string, p *Principal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[h] = &cacheEntry{
		principal: *p,
		keyID:     p.KeyID,
		orgID:     p.OrganizationID,
		userID:    p.UserID,
		expiresAt: c.now().Add(c.ttl),
	}
}

// EvictKey removes the cache entry for an apikey id (ctrl:apikey.revoked).
func (c *CachingKeyVerifier) EvictKey(keyID string) {
	if keyID == "" {
		return
	}
	c.evictBy(func(e *cacheEntry) bool { return e.keyID == keyID })
}

// EvictUser removes every cache entry owned by a user (ctrl:user.banned).
// api-key principals carry no UserID, so this matters once user-scoped entries
// exist; it is wired now so the control bus has a complete eviction surface.
func (c *CachingKeyVerifier) EvictUser(userID string) {
	if userID == "" {
		return
	}
	c.evictBy(func(e *cacheEntry) bool { return e.userID == userID })
}

// EvictOrg removes every cache entry scoped to an organization
// (ctrl:member.removed narrows this to a (userID, orgID) pair via EvictUserOrg).
func (c *CachingKeyVerifier) EvictOrg(orgID string) {
	if orgID == "" {
		return
	}
	c.evictBy(func(e *cacheEntry) bool { return e.orgID == orgID })
}

// EvictUserOrg removes entries for a user within one org (ctrl:member.removed).
func (c *CachingKeyVerifier) EvictUserOrg(userID, orgID string) {
	if userID == "" || orgID == "" {
		return
	}
	c.evictBy(func(e *cacheEntry) bool { return e.userID == userID && e.orgID == orgID })
}

// evictBy deletes every entry matching pred.
func (c *CachingKeyVerifier) evictBy(pred func(*cacheEntry) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for h, e := range c.entries {
		if pred(e) {
			delete(c.entries, h)
		}
	}
}

// cacheKeyHash hashes a raw api-key for use as the cache map key, so the raw
// secret is never held in memory as a map key. This is independent of the
// better-auth storage Hasher — it only needs to be a stable, collision-resistant
// digest of the presented secret.
func cacheKeyHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
