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

// RemoteKeySet is the gateway's concurrency-safe cache of the router's public
// assertion keys. KeySet serves a fresh cached set without I/O, refreshes a stale
// set synchronously, and falls back to a stale non-empty set when that refresh
// fails so a transient router outage does not stop already-verifiable traffic.
//
// Refresh is the forced key-rotation path used after signature verification
// encounters an unknown key ID. A minimum refresh interval limits attacker-driven
// fetches, while a context-aware refresh gate and generation counter coalesce
// concurrent callers into one network request. Gate release happens before the
// next waiter checks the published generation under mu; canceled waiters leave
// immediately instead of waiting for unrelated network I/O. Empty fetched sets
// are rejected and never replace usable trust material.
type RemoteKeySet struct {
	url          string
	fetch        Fetcher
	now          func() time.Time
	refreshEvery time.Duration
	minRefresh   time.Duration

	mu          sync.RWMutex
	set         jwk.Set
	fetchedAt   time.Time
	generation  uint64
	refreshGate chan struct{}
	lastRefresh time.Time // protected by refreshGate
}

// RemoteKeySetOption configures cache freshness and refresh throttling before the
// source is first used. NewRemoteKeySet validates the resulting durations and
// dependencies, so options cannot leave a partially usable source.
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
		refreshGate:  make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(r)
	}
	if r.fetch == nil || r.now == nil {
		return nil, errors.New("assertion: jwks source dependencies must not be nil")
	}
	if r.refreshEvery <= 0 || r.minRefresh < 0 {
		return nil, errors.New("assertion: jwks refresh intervals are invalid")
	}
	return r, nil
}

// KeySet returns keys suitable for an immediate verification attempt. The first
// caller fetches synchronously; concurrent first callers wait for and reuse that
// result. Once populated, a failed age-based refresh returns the prior set, but a
// source with no successful fetch returns the fetch error.
func (r *RemoteKeySet) KeySet(ctx context.Context) (jwk.Set, error) {
	r.mu.RLock()
	set, fetchedAt, generation := r.set, r.fetchedAt, r.generation
	r.mu.RUnlock()
	if set != nil && r.now().Sub(fetchedAt) < r.refreshEvery {
		return set, nil
	}
	refreshed, err := r.refresh(ctx, generation)
	if err != nil {
		if set != nil {
			return set, nil // serve stale rather than fail closed on a transient fetch error
		}
		return nil, err
	}
	return refreshed, nil
}

// Refresh requests a new JWKS for signature-key rotation. It returns the current
// cached set when the request is throttled or another goroutine already refreshed
// the observed generation; callers still perform signature verification, so
// returning the old set cannot grant trust to an unknown key.
func (r *RemoteKeySet) Refresh(ctx context.Context) (jwk.Set, error) {
	r.mu.RLock()
	generation := r.generation
	r.mu.RUnlock()
	return r.refresh(ctx, generation)
}

func (r *RemoteKeySet) refresh(ctx context.Context, observedGeneration uint64) (jwk.Set, error) {
	select {
	case r.refreshGate <- struct{}{}:
		defer func() { <-r.refreshGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	r.mu.RLock()
	cur, generation := r.set, r.generation
	r.mu.RUnlock()
	if generation != observedGeneration {
		return cur, nil // another caller already completed this refresh
	}
	if !r.lastRefresh.IsZero() && r.now().Sub(r.lastRefresh) < r.minRefresh {
		if cur == nil {
			return nil, errors.New("assertion: jwks refresh rate-limited and no cache")
		}
		return cur, nil
	}
	r.lastRefresh = r.now()

	set, err := r.fetch(ctx, r.url)
	if err != nil {
		return nil, fmt.Errorf("assertion: fetch router jwks: %w", err)
	}
	if set == nil || set.Len() == 0 {
		return nil, errors.New("assertion: fetched router jwks is empty")
	}
	r.mu.Lock()
	r.set = set
	r.fetchedAt = r.now()
	r.generation++
	r.mu.Unlock()
	return set, nil
}
