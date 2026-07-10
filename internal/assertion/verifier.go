package assertion

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwt"
)

// Binding is the actual proxied request the gateway re-checks the assertion
// against: a captured assertion is useless unless replayed verbatim — same
// method, same target, same body.
type Binding struct {
	Method string
	Path   string
	Body   []byte
}

// Verifier is the gateway side of the internal trust boundary. It owns no private
// signing material: each Verify checks the router JWKS signature, expected router
// issuer, this gateway's audience, time claims, exact request binding, coherent
// identity fields, and one-time nonce before returning a Principal.
//
// A Verifier is safe for concurrent requests when its KeySetSource is safe; the
// built-in RemoteKeySet and NonceCache are. Verification errors intentionally
// retain detail only inside this package—the HTTP middleware emits one generic
// unauthorized response so callers cannot probe trust configuration.
type Verifier struct {
	source    KeySetSource
	issuer    string // expected iss (the router identity)
	gatewayID string // expected aud (this gateway's id)
	nonce     *NonceCache
	skew      time.Duration
}

// VerifierOption configures a Verifier.
type VerifierOption func(*Verifier)

// WithSkew sets the acceptable clock skew for exp/iat (default DefaultSkew).
func WithSkew(d time.Duration) VerifierOption { return func(v *Verifier) { v.skew = d } }

// WithNonceCache injects the anti-replay cache (default: a fresh in-memory cache).
func WithNonceCache(n *NonceCache) VerifierOption { return func(v *Verifier) { v.nonce = n } }

// NewVerifier builds a gateway-side assertion verifier. issuer is the expected
// router identity (`iss`); gatewayID is this gateway's id (the enforced `aud`).
func NewVerifier(source KeySetSource, issuer, gatewayID string, opts ...VerifierOption) (*Verifier, error) {
	if source == nil {
		return nil, errors.New("assertion: verifier needs a key set source")
	}
	if issuer == "" || gatewayID == "" {
		return nil, errors.New("assertion: verifier needs issuer and gateway id")
	}
	v := &Verifier{
		source:    source,
		issuer:    issuer,
		gatewayID: gatewayID,
		nonce:     NewNonceCache(),
		skew:      DefaultSkew,
	}
	for _, o := range opts {
		o(v)
	}
	if v.nonce == nil {
		return nil, errors.New("assertion: verifier nonce cache must not be nil")
	}
	if v.skew < 0 {
		return nil, errors.New("assertion: verifier skew must not be negative")
	}
	return v, nil
}

// Verify validates raw against the exact request observed by the gateway. Binding
// checks run before the nonce is consumed so a malformed replay cannot burn a
// legitimate assertion; identity coherence is checked before the atomic nonce
// insertion. A successful call consumes the token and all later calls fail.
func (v *Verifier) Verify(ctx context.Context, raw string, bind Binding) (*Principal, error) {
	tok, err := v.parse(ctx, raw)
	if err != nil {
		return nil, err
	}

	// Request binding: a captured assertion must be replayed verbatim or it is
	// rejected before the nonce is even consulted.
	if got := claimString(tok, claimMethod); got != bind.Method {
		return nil, fmt.Errorf("assertion: method mismatch (asserted %q, request %q)", got, bind.Method)
	}
	if got := claimString(tok, claimPath); got != bind.Path {
		return nil, fmt.Errorf("assertion: path mismatch")
	}
	if got := claimString(tok, claimBody); got != BodyHash(bind.Body) {
		return nil, errors.New("assertion: body hash mismatch")
	}

	p := principalFromToken(tok)
	if err := validatePrincipal(*p); err != nil {
		return nil, err
	}

	// Anti-replay: one-time jti within the freshness window.
	jti, _ := tok.JwtID()
	exp, ok := tok.Expiration()
	if !ok {
		return nil, errors.New("assertion: missing expiry")
	}
	if v.nonce.SeenBefore(jti, exp) {
		return nil, errors.New("assertion: replay detected")
	}

	return p, nil
}

// parse verifies the signature + standard claims, retrying once after a JWKS
// refresh so key rotation (an unknown kid in the cache) recovers transparently.
func (v *Verifier) parse(ctx context.Context, raw string) (jwt.Token, error) {
	set, err := v.source.KeySet(ctx)
	if err != nil {
		return nil, fmt.Errorf("assertion: load router jwks: %w", err)
	}
	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKeySet(set),
		jwt.WithValidate(true),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.gatewayID),
		jwt.WithAcceptableSkew(v.skew),
	)
	if err == nil {
		return tok, nil
	}
	// One retry after a forced refresh handles router key rotation.
	refreshed, rErr := v.source.Refresh(ctx)
	if rErr != nil {
		return nil, fmt.Errorf("assertion: verify (refresh failed: %v): %w", rErr, err)
	}
	tok, err2 := jwt.Parse([]byte(raw),
		jwt.WithKeySet(refreshed),
		jwt.WithValidate(true),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.gatewayID),
		jwt.WithAcceptableSkew(v.skew),
	)
	if err2 != nil {
		return nil, fmt.Errorf("assertion: verify: %w", err2)
	}
	return tok, nil
}

func claimString(tok jwt.Token, name string) string {
	var s string
	_ = tok.Get(name, &s)
	return s
}

func principalFromToken(tok jwt.Token) *Principal {
	return &Principal{
		Kind:           claimString(tok, claimKind),
		OrganizationID: claimString(tok, claimOrg),
		UserID:         claimString(tok, claimUserID),
		OrgRole:        claimString(tok, claimOrgRole),
		PlatformRole:   claimString(tok, claimRole),
		KeyID:          claimString(tok, claimKeyID),
		Permissions:    permsFromString(claimString(tok, claimPerms)),
	}
}
