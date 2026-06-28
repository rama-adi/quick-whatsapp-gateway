package assertion

import (
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// Minter signs internal assertions on the router. It holds the Ed25519 private key
// and stamps each token with the resolved principal + the request binding. One
// Minter is shared across all proxied requests.
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

// Mint produces a signed, request-bound assertion for one proxied request.
func (m *Minter) Mint(p Principal, req Request) (string, error) {
	if req.Gateway == "" {
		return "", fmt.Errorf("assertion: mint requires a target gateway (aud)")
	}
	now := m.now()
	b := jwt.NewBuilder().
		Issuer(m.issuer).
		Audience([]string{req.Gateway}).
		IssuedAt(now).
		Expiration(now.Add(m.ttl)).
		JwtID(m.newJTI()).
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
