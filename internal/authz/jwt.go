package authz

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// TokenVerifier verifies a raw JWT string and resolves it to a Principal. It is
// the consumer-defined abstraction the auth middleware depends on (§4.3); the
// concrete implementation is *JWTVerifier.
type TokenVerifier interface {
	// VerifyToken verifies the signature, issuer, audience and expiry of raw and
	// returns the human Principal it encodes. It returns an error on any failure;
	// callers map that to 401 without leaking detail.
	VerifyToken(ctx context.Context, raw string) (*Principal, error)
}

// Claim names. better-auth's JWT plugin lets the frontend customize the payload
// via definePayload; v2 configures it to carry the active organization and the
// member's org role alongside the platform role (§4.1).
const (
	claimActiveOrg = "activeOrganizationId"
	claimOrgRole   = "orgRole"
	claimRole      = "role" // better-auth admin-plugin platform role (e.g. super_admin)
)

// jwksFetcher fetches a JWK set from a URL. Abstracted so tests can inject a
// fake/httptest source; production uses jwkFetchFunc backed by jwk.Fetch.
type jwksFetcher func(ctx context.Context, url string) (jwk.Set, error)

func jwkFetchFunc(ctx context.Context, url string) (jwk.Set, error) {
	return jwk.Fetch(ctx, url)
}

// JWTVerifier verifies better-auth JWTs locally against a concurrency-safe JWKS
// cache (§4.1). It enforces signature, issuer, audience, standard time claims,
// and a non-empty subject before it constructs a human Principal. Optional
// organization and role claims affect later authorization but never bypass the
// cryptographic checks.
//
// The first verification fetches keys synchronously. Later calls use cached keys,
// lazily refresh stale material, and trigger a rate-limited refresh for an unknown
// key ID. Concurrent refreshes are coalesced by generation behind a context-aware
// gate, so a cold start cannot stampede better-auth and canceled waiters do not
// remain blocked behind another request's fetch. Gate release happens before the
// next waiter checks the generation, while mu protects publication of the set.
// Transient refresh failures may use an existing non-empty set; an empty trust
// store always fails closed.
type JWTVerifier struct {
	jwksURL string
	issuer  string // == audience == BETTER_AUTH_URL
	fetch   jwksFetcher
	now     func() time.Time

	// refreshEvery bounds cache staleness; minRefresh rate-limits refreshes
	// triggered by unknown kids so a flood of bad tokens can't hammer the JWKS.
	refreshEvery time.Duration
	minRefresh   time.Duration

	mu          sync.RWMutex
	set         jwk.Set
	fetchedAt   time.Time
	generation  uint64
	refreshGate chan struct{}
	lastRefresh time.Time // protected by refreshGate
}

// JWTVerifierOption configures cache age and attacker-triggered refresh limits.
// Options are applied only during construction and validated before the verifier
// is returned.
type JWTVerifierOption func(*JWTVerifier)

// WithRefreshInterval sets how long a cached JWKS is served before a lazy
// refresh on the next verify. Default 1h.
func WithRefreshInterval(d time.Duration) JWTVerifierOption {
	return func(v *JWTVerifier) { v.refreshEvery = d }
}

// WithMinRefreshInterval rate-limits unknown-kid refreshes. Default 1m.
func WithMinRefreshInterval(d time.Duration) JWTVerifierOption {
	return func(v *JWTVerifier) { v.minRefresh = d }
}

// withFetcher injects the JWKS fetcher (tests).
func withFetcher(f jwksFetcher) JWTVerifierOption {
	return func(v *JWTVerifier) { v.fetch = f }
}

// withClock injects the time source (tests).
func withClock(now func() time.Time) JWTVerifierOption {
	return func(v *JWTVerifier) { v.now = now }
}

// NewJWTVerifier builds a verifier for the given JWKS URL and better-auth base
// URL (the enforced iss/aud). Both must be non-empty.
func NewJWTVerifier(jwksURL, betterAuthURL string, opts ...JWTVerifierOption) (*JWTVerifier, error) {
	if jwksURL == "" {
		return nil, errors.New("authz: jwks url must not be empty")
	}
	if betterAuthURL == "" {
		return nil, errors.New("authz: better-auth url (iss/aud) must not be empty")
	}
	v := &JWTVerifier{
		jwksURL:      jwksURL,
		issuer:       betterAuthURL,
		fetch:        jwkFetchFunc,
		now:          time.Now,
		refreshEvery: time.Hour,
		minRefresh:   time.Minute,
		refreshGate:  make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(v)
	}
	if v.fetch == nil || v.now == nil {
		return nil, errors.New("authz: jwks verifier dependencies must not be nil")
	}
	if v.refreshEvery <= 0 || v.minRefresh < 0 {
		return nil, errors.New("authz: jwks refresh intervals are invalid")
	}
	return v, nil
}

// VerifyToken implements TokenVerifier. It verifies signature (against the kid's
// public key), iss/aud == BETTER_AUTH_URL, and expiry, then extracts the human
// Principal. EdDSA/Ed25519 is the default; ES256/RS256 are also accepted because
// the key set advertises each key's algorithm and jwx selects accordingly.
func (v *JWTVerifier) VerifyToken(ctx context.Context, raw string) (*Principal, error) {
	set, err := v.keySetFor(ctx, raw)
	if err != nil {
		return nil, err
	}

	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKeySet(set),
		jwt.WithValidate(true),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.issuer),
	)
	if err != nil {
		return nil, fmt.Errorf("authz: verify jwt: %w", err)
	}

	sub, ok := tok.Subject()
	if !ok || sub == "" {
		return nil, errors.New("authz: jwt missing sub claim")
	}

	p := &Principal{Kind: KindUser, UserID: sub}
	// definePayload claims are optional on the wire; absence is not fatal (a user
	// with no active org simply reaches no org-scoped resources).
	_ = tok.Get(claimActiveOrg, &p.OrganizationID)
	_ = tok.Get(claimOrgRole, &p.OrgRole)
	_ = tok.Get(claimRole, &p.PlatformRole)
	return p, nil
}

// keySetFor selects trust material for one verification. The key ID is read from
// the unverified protected header only as a cache hint; it never establishes
// identity or authorization. Unknown IDs can request a bounded refresh, after
// which VerifyToken still requires a valid signature from the returned set.
func (v *JWTVerifier) keySetFor(ctx context.Context, raw string) (jwk.Set, error) {
	kid := protectedKeyID(raw)

	set, fetchedAt, generation := v.cached()
	stale := set == nil || v.now().Sub(fetchedAt) >= v.refreshEvery
	unknownKid := set != nil && kid != "" && !hasKey(set, kid)

	if stale || unknownKid {
		refreshed, err := v.refresh(ctx, unknownKid, generation)
		switch {
		case err == nil:
			set = refreshed
		case set == nil:
			// No usable cache to fall back on.
			return nil, err
			// else: serve the stale set and let jwt.Parse fail if the kid is truly absent.
		}
	}
	if set == nil {
		return nil, errors.New("authz: no jwks available")
	}
	return set, nil
}

// refresh serializes network fetches and swaps in only non-empty key sets. The
// observed generation lets waiters reuse a refresh completed while they waited;
// unknown-key requests are additionally throttled so attacker-controlled token
// headers cannot amplify traffic to the identity service.
func (v *JWTVerifier) refresh(ctx context.Context, rateLimited bool, observedGeneration uint64) (jwk.Set, error) {
	select {
	case v.refreshGate <- struct{}{}:
		defer func() { <-v.refreshGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	cur, _, generation := v.cached()
	if generation != observedGeneration {
		return cur, nil // another caller already completed this refresh
	}
	if rateLimited && !v.lastRefresh.IsZero() && v.now().Sub(v.lastRefresh) < v.minRefresh {
		if cur == nil {
			return nil, errors.New("authz: jwks refresh rate-limited and no cache")
		}
		return cur, nil
	}
	v.lastRefresh = v.now()

	set, err := v.fetch(ctx, v.jwksURL)
	if err != nil {
		return nil, fmt.Errorf("authz: fetch jwks: %w", err)
	}
	if set == nil || set.Len() == 0 {
		return nil, errors.New("authz: fetched jwks is empty")
	}
	v.mu.Lock()
	v.set = set
	v.fetchedAt = v.now()
	v.generation++
	v.mu.Unlock()
	return set, nil
}

func (v *JWTVerifier) cached() (jwk.Set, time.Time, uint64) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.set, v.fetchedAt, v.generation
}

// protectedKeyID extracts the `kid` from a compact JWS without verifying it.
// Returns "" if the token can't be parsed or carries no kid.
func protectedKeyID(raw string) string {
	msg, err := jws.Parse([]byte(raw))
	if err != nil {
		return ""
	}
	sigs := msg.Signatures()
	if len(sigs) == 0 {
		return ""
	}
	kid, _ := sigs[0].ProtectedHeaders().KeyID()
	return kid
}

func hasKey(set jwk.Set, kid string) bool {
	_, ok := set.LookupKeyID(kid)
	return ok
}
