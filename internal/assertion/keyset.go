package assertion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
)

// KeySetSource supplies the public JWKS the verifier checks assertion signatures
// against. KeySet returns the current (cached) set; Refresh forces a re-fetch so
// the verifier can recover from key rotation on an unknown-kid miss. It mirrors
// the JWKS-cache contract the gateway used for better-auth keys (D3: same pattern,
// repointed at the router's JWKS).
type KeySetSource interface {
	KeySet(ctx context.Context) (jwk.Set, error)
	Refresh(ctx context.Context) (jwk.Set, error)
}

// StaticKeySet is a fixed in-memory KeySetSource — used in-process (the router can
// verify its own assertions in tests) and in unit tests. Refresh is a no-op.
type StaticKeySet struct{ Set jwk.Set }

func (s StaticKeySet) KeySet(context.Context) (jwk.Set, error)  { return s.Set, nil }
func (s StaticKeySet) Refresh(context.Context) (jwk.Set, error) { return s.Set, nil }

// Fetcher fetches a JWKS from a URL. Abstracted so tests can inject a fake;
// production uses jwk.Fetch.
type Fetcher func(ctx context.Context, url string) (jwk.Set, error)

func defaultFetch(ctx context.Context, url string) (jwk.Set, error) { return jwk.Fetch(ctx, url) }

// RemoteKeySet caches the router's JWKS fetched from ROUTER_JWKS_URL, refreshing
// lazily once the cache is older than refreshEvery and on demand (Refresh) when a
// token carries a kid the cache doesn't know. Refresh is rate-limited by
// minRefresh so a flood of unknown-kid tokens can't hammer the router.
type RemoteKeySet struct {
	url          string
	fetch        Fetcher
	now          func() time.Time
	refreshEvery time.Duration
	minRefresh   time.Duration

	mu          sync.RWMutex
	set         jwk.Set
	fetchedAt   time.Time
	lastRefresh time.Time
}

// RemoteKeySetOption configures a RemoteKeySet.
type RemoteKeySetOption func(*RemoteKeySet)

func WithRefreshInterval(d time.Duration) RemoteKeySetOption {
	return func(r *RemoteKeySet) { r.refreshEvery = d }
}
func WithMinRefreshInterval(d time.Duration) RemoteKeySetOption {
	return func(r *RemoteKeySet) { r.minRefresh = d }
}
func withFetcher(f Fetcher) RemoteKeySetOption { return func(r *RemoteKeySet) { r.fetch = f } }

// NewRemoteKeySet builds a cached JWKS source for the given URL.
func NewRemoteKeySet(url string, opts ...RemoteKeySetOption) (*RemoteKeySet, error) {
	if url == "" {
		return nil, errors.New("assertion: router jwks url must not be empty")
	}
	r := &RemoteKeySet{
		url:          url,
		fetch:        defaultFetch,
		now:          time.Now,
		refreshEvery: time.Hour,
		minRefresh:   time.Minute,
	}
	for _, o := range opts {
		o(r)
	}
	return r, nil
}

// KeySet returns the cached set, lazily refreshing it when stale. If nothing is
// cached yet it fetches synchronously.
func (r *RemoteKeySet) KeySet(ctx context.Context) (jwk.Set, error) {
	r.mu.RLock()
	set, fetchedAt := r.set, r.fetchedAt
	r.mu.RUnlock()
	if set != nil && r.now().Sub(fetchedAt) < r.refreshEvery {
		return set, nil
	}
	refreshed, err := r.Refresh(ctx)
	if err != nil {
		if set != nil {
			return set, nil // serve stale rather than fail closed on a transient fetch error
		}
		return nil, err
	}
	return refreshed, nil
}

// Refresh fetches a fresh JWKS and swaps it in, rate-limited by minRefresh so an
// unknown-kid retry storm can't hammer the router. When rate-limited it returns
// the current cached set.
func (r *RemoteKeySet) Refresh(ctx context.Context) (jwk.Set, error) {
	r.mu.Lock()
	if !r.lastRefresh.IsZero() && r.now().Sub(r.lastRefresh) < r.minRefresh {
		cur := r.set
		r.mu.Unlock()
		if cur == nil {
			return nil, errors.New("assertion: jwks refresh rate-limited and no cache")
		}
		return cur, nil
	}
	r.lastRefresh = r.now()
	r.mu.Unlock()

	set, err := r.fetch(ctx, r.url)
	if err != nil {
		return nil, fmt.Errorf("assertion: fetch router jwks: %w", err)
	}
	r.mu.Lock()
	r.set = set
	r.fetchedAt = r.now()
	r.mu.Unlock()
	return set, nil
}
