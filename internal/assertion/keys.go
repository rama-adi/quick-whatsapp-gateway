package assertion

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

// ParsePrivateKey decodes the router's Ed25519 signing key from a base64 string.
// Both the 32-byte seed and the full 64-byte private key are accepted (base64
// standard or URL alphabet, padded or not), so operators can paste whichever
// `openssl`/`age`-style output they have. The derived public key's deterministic
// kid is returned alongside.
func ParsePrivateKey(encoded string) (ed25519.PrivateKey, string, error) {
	raw, err := decodeBase64Any(encoded)
	if err != nil {
		return nil, "", fmt.Errorf("assertion: decode private key: %w", err)
	}
	var priv ed25519.PrivateKey
	switch len(raw) {
	case ed25519.SeedSize: // 32-byte seed
		priv = ed25519.NewKeyFromSeed(raw)
	case ed25519.PrivateKeySize: // 64-byte full private key
		priv = ed25519.PrivateKey(raw)
	default:
		return nil, "", fmt.Errorf("assertion: private key must be %d (seed) or %d (full) bytes, got %d",
			ed25519.SeedSize, ed25519.PrivateKeySize, len(raw))
	}
	return priv, KeyID(priv.Public().(ed25519.PublicKey)), nil
}

// KeyID derives a stable key id from an Ed25519 public key: the unpadded
// base64url of SHA-256(pubkey). The router stamps it on signed assertions and
// advertises it in its JWKS; the gateway matches by it.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// signingKey builds the private jwk.Key used to sign assertions, with its kid and
// EdDSA algorithm set.
func signingKey(priv ed25519.PrivateKey, kid string) (jwk.Key, error) {
	key, err := jwk.Import(priv)
	if err != nil {
		return nil, fmt.Errorf("assertion: import private key: %w", err)
	}
	if err := key.Set(jwk.KeyIDKey, kid); err != nil {
		return nil, err
	}
	if err := key.Set(jwk.AlgorithmKey, jwa.EdDSA()); err != nil {
		return nil, err
	}
	return key, nil
}

// PublicJWKS returns the single-key public JWK set the router serves at its JWKS
// endpoint for gateways to verify assertions against.
func PublicJWKS(priv ed25519.PrivateKey, kid string) (jwk.Set, error) {
	signer, err := signingKey(priv, kid)
	if err != nil {
		return nil, err
	}
	pub, err := signer.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("assertion: derive public key: %w", err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		return nil, err
	}
	return set, nil
}

// decodeBase64Any tries the URL and standard base64 alphabets, padded and raw, so
// callers need not care which encoding their key material is in.
func decodeBase64Any(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("not valid base64 (url/std, padded/raw)")
}
