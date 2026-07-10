package assertion

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// Minter is the only component that holds the router's Ed25519 private assertion
// key. A single immutable instance is safe to share across requests: each Mint
// call snapshots the verified principal, target gateway, request target, method,
// and body digest into a short-lived token with a fresh nonce.
//
// Construction rejects invalid key material, issuer, lifetime, clock, or nonce
// source. Mint rejects partial identities and incomplete bindings before signing,
// preventing the router from emitting assertions the gateway cannot safely scope.
type Minter struct {
	signer jwk.Key
	kid    string
	issuer string
	ttl    time.Duration
	now    func() time.Time
	newJTI func() string
}

// MinterOption configures a Minter.
type MinterOption func(*Minter)

// WithTTL sets the assertion freshness window (default DefaultTTL).
func WithTTL(d time.Duration) MinterOption { return func(m *Minter) { m.ttl = d } }

// withMinterClock injects the time source (tests).
func withMinterClock(now func() time.Time) MinterOption { return func(m *Minter) { m.now = now } }

// withJTIFunc injects the nonce generator (tests).
func withJTIFunc(f func() string) MinterOption { return func(m *Minter) { m.newJTI = f } }

// NewMinter builds a Minter from the router's Ed25519 private key. issuer is the
// stable router identity stamped as `iss` (and enforced by the gateway verifier).
func NewMinter(priv ed25519.PrivateKey, issuer string, opts ...MinterOption) (*Minter, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("assertion: minter needs a %d-byte private key", ed25519.PrivateKeySize)
	}
	if issuer == "" {
		return nil, fmt.Errorf("assertion: minter issuer must not be empty")
	}
	kid := KeyID(priv.Public().(ed25519.PublicKey))
	signer, err := signingKey(priv, kid)
	if err != nil {
		return nil, err
	}
	m := &Minter{
		signer: signer,
		kid:    kid,
		issuer: issuer,
		ttl:    DefaultTTL,
		now:    time.Now,
		newJTI: domain.NewEventID, // any unguessable, collision-free token works as a nonce
	}
	for _, o := range opts {
		o(m)
	}
	if m.ttl <= 0 {
		return nil, errors.New("assertion: minter ttl must be positive")
	}
	if m.now == nil || m.newJTI == nil {
		return nil, errors.New("assertion: minter dependencies must not be nil")
	}
	return m, nil
}

// KeyID returns the kid the minter signs with (also the kid in the published JWKS).
func (m *Minter) KeyID() string { return m.kid }

// JWKS returns the public key set the router serves so gateways can verify.
func (m *Minter) JWKS() (jwk.Set, error) {
	pub, err := m.signer.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("assertion: derive public key: %w", err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		return nil, err
	}
	return set, nil
}

// Mint produces one signed assertion for an already authenticated request. It
// does not perform end-user authentication; callers must pass the Principal from
// the router auth middleware and the exact bytes that will be proxied. The result
// is intended for one immediate redemption because the verifier enforces expiry
// and nonce uniqueness.
func (m *Minter) Mint(p Principal, req Request) (string, error) {
	if err := validatePrincipal(p); err != nil {
		return "", err
	}
	if req.Gateway == "" || req.Method == "" || req.Path == "" {
		return "", errors.New("assertion: mint requires gateway, method, and path")
	}
	now := m.now()
	jti := m.newJTI()
	if jti == "" {
		return "", errors.New("assertion: mint generated an empty nonce")
	}
	b := jwt.NewBuilder().
		Issuer(m.issuer).
		Audience([]string{req.Gateway}).
		IssuedAt(now).
		Expiration(now.Add(m.ttl)).
		JwtID(jti).
		Subject(p.UserID).
		Claim(claimOrg, p.OrganizationID).
		Claim(claimKind, p.Kind).
		Claim(claimUserID, p.UserID).
		Claim(claimOrgRole, p.OrgRole).
		Claim(claimRole, p.PlatformRole).
		Claim(claimKeyID, p.KeyID).
		Claim(claimPerms, permsToString(p.Permissions)).
		Claim(claimSession, req.Session).
		Claim(claimMethod, req.Method).
		Claim(claimPath, req.Path).
		Claim(claimBody, BodyHash(req.Body))

	tok, err := b.Build()
	if err != nil {
		return "", fmt.Errorf("assertion: build token: %w", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), m.signer))
	if err != nil {
		return "", fmt.Errorf("assertion: sign token: %w", err)
	}
	return string(signed), nil
}

// validatePrincipal enforces the wire identity invariants shared by minting and
// verification. User principals require a subject, while API-key principals are
// always organization-scoped and retain a key ID for audit and revocation.
func validatePrincipal(p Principal) error {
	switch p.Kind {
	case "user":
		if p.UserID == "" {
			return errors.New("assertion: user principal requires a user id")
		}
	case "apikey":
		if p.OrganizationID == "" || p.KeyID == "" {
			return errors.New("assertion: api-key principal requires organization and key ids")
		}
	default:
		return fmt.Errorf("assertion: unsupported principal kind %q", p.Kind)
	}
	return nil
}
