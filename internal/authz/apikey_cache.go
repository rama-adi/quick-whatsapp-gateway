package authz

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// CachingKeyVerifier is a positive cache in front of a KeyVerifier (§4.6). A
// busy client should not cost a MySQL lookup per request, and a brief DB blip
// shouldn't drop in-flight callers — so a successfully-verified key is cached
// for a short TTL (~60s). The cache is FAIL-CLOSED: only successes are cached
// (an error is never memoized), and the short TTL is the backstop — even if a
// router misses a control-bus revocation, the entry ages out within the window
// and the next refresh fails closed against the (now gone) `apikey` row.
//
// The cache is keyed by the SHA-256 of the raw key (never the raw key itself)
// and is also indexed by keyId, userId and organizationId so the control bus can
// evict precisely on apikey.revoked / user.banned / member.removed.
//
// The mutex protects entries and an eviction generation, but is never held across
// the delegate's database lookup. Eviction always advances the generation—even
// when the matching key is not cached—so an in-flight lookup that started before
// revocation must revalidate and cannot repopulate stale authorization.
// *CachingKeyVerifier satisfies KeyVerifier and drops into Authenticate in place
// of the bare verifier.
type CachingKeyVerifier struct {
	inner KeyVerifier
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	entries map[string]*cacheEntry // keyed by raw-key hash
	// generation advances for every control-bus eviction, including a cache miss.
	// A verification that began earlier must re-check the database before it can
	// publish a positive result, preventing revocation/cache-fill races.
	generation uint64
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

// VerifyKey implements KeyVerifier. Hits are copied under the cache mutex; misses
// call the delegate without holding that mutex so database latency never blocks
// unrelated hits or evictions. Before the lookup, it snapshots the eviction
// generation. If any eviction happens before publication, the lookup is repeated
// against the authoritative store rather than resurrecting a just-revoked key.
// Errors and structurally invalid principals are never cached.
func (c *CachingKeyVerifier) VerifyKey(ctx context.Context, raw string) (*Principal, error) {
	if c.inner == nil {
		return nil, errors.New("authz: cached api-key verifier has no delegate")
	}
	if raw == "" {
		// Delegate so the inner verifier owns the canonical error.
		return c.inner.VerifyKey(ctx, raw)
	}
	h := cacheKeyHash(raw)

	if p, ok := c.lookup(h); ok {
		return p, nil
	}

	for {
		generation := c.currentGeneration()
		p, err := c.inner.VerifyKey(ctx, raw)
		if err != nil {
			return nil, err
		}
		if !validPrincipal(p, KindAPIKey) {
			return nil, errors.New("authz: api-key verifier returned an invalid principal")
		}
		if c.storeIfGeneration(h, p, generation) {
			cp := *p
			return &cp, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
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

// storeIfGeneration publishes a verified Principal only if no eviction happened
// since the authoritative lookup began. The mutex unlock synchronizes this write
// with later lookups and with generation increments in evictBy.
func (c *CachingKeyVerifier) storeIfGeneration(h string, p *Principal, generation uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return false
	}
	c.entries[h] = &cacheEntry{
		principal: *p,
		keyID:     p.KeyID,
		orgID:     p.OrganizationID,
		userID:    p.UserID,
		expiresAt: c.now().Add(c.ttl),
	}
	return true
}

func (c *CachingKeyVerifier) currentGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
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
	c.generation++
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
