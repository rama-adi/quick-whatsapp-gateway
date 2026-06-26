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

// JWTVerifier verifies better-auth JWTs locally against a cached JWKS (§4.1). It
// fetches the JWKS on first use, caches the whole set (keyed internally by kid by
// the jwk.Set), and refreshes it (a) when a token's kid is not in the cache and
// (b) no more often than minRefresh, plus (c) lazily once the set is older than
// refreshEvery. Verification never calls the frontend on the hot path.
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
	lastRefresh time.Time
}

// JWTVerifierOption configures a JWTVerifier.
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
	}
	for _, o := range opts {
		o(v)
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

// keySetFor returns the cached JWKS, refreshing it first if the token's kid is
// not yet known (rate-limited) or if the cache is stale. The kid is read from the
// JWS protected header WITHOUT verifying the signature; the actual verification
// in VerifyToken is what establishes trust.
func (v *JWTVerifier) keySetFor(ctx context.Context, raw string) (jwk.Set, error) {
	kid := protectedKeyID(raw)

	set, fetchedAt := v.cached()
	stale := set == nil || v.now().Sub(fetchedAt) >= v.refreshEvery
	unknownKid := set != nil && kid != "" && !hasKey(set, kid)

	if stale || unknownKid {
		refreshed, err := v.refresh(ctx, unknownKid)
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

// refresh fetches a fresh JWKS and swaps it in. When rateLimited is true (an
// unknown-kid trigger), it skips the fetch if the last refresh was too recent,
// returning the current cached set so a burst of bad kids can't hammer the JWKS.
func (v *JWTVerifier) refresh(ctx context.Context, rateLimited bool) (jwk.Set, error) {
	v.mu.Lock()
	if rateLimited && !v.lastRefresh.IsZero() && v.now().Sub(v.lastRefresh) < v.minRefresh {
		cur := v.set
		v.mu.Unlock()
		if cur == nil {
			return nil, errors.New("authz: jwks refresh rate-limited and no cache")
		}
		return cur, nil
	}
	v.lastRefresh = v.now()
	v.mu.Unlock()

	set, err := v.fetch(ctx, v.jwksURL)
	if err != nil {
		return nil, fmt.Errorf("authz: fetch jwks: %w", err)
	}
	v.mu.Lock()
	v.set = set
	v.fetchedAt = v.now()
	v.mu.Unlock()
	return set, nil
}

func (v *JWTVerifier) cached() (jwk.Set, time.Time) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.set, v.fetchedAt
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
