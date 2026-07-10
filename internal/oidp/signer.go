package oidp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

const (
	SigningAlg = "EdDSA"
	KeyActive  = "active"
	KeyNext    = "next"
	KeyRetired = "retired"
)

// SigningKeyRepo stores encrypted Ed25519 private keys and publishable JWKs.
// PromoteNext and Retire must preserve the database invariant that exactly one
// key is active after a completed rotation step.
type SigningKeyRepo interface {
	Create(context.Context, domain.OAuthSigningKey) error
	GetActive(context.Context) (domain.OAuthSigningKey, error)
	ListPublic(context.Context) ([]domain.OAuthSigningKey, error)
	CountByStatus(context.Context, string) (int, error)
	PromoteNext(context.Context, string, int64) error
	Retire(context.Context, string, int64) error
}

// Signer issues compact EdDSA JWTs from the active encrypted key and publishes
// all verification keys through JWKS. The decrypted private key is cached behind
// mu; Invalidate is the explicit happens-before boundary after key rotation.
type Signer struct {
	repo   SigningKeyRepo
	aead   cipher.AEAD
	now    func() time.Time
	mu     sync.RWMutex
	cached *cachedKey
}

type cachedKey struct {
	kid string
	key ed25519.PrivateKey
}

// NewSigner derives the AEAD used for private-key encryption. It does not load or
// decrypt a signing key until the first SignJWT call.
func NewSigner(repo SigningKeyRepo, encKey string) (*Signer, error) {
	aead, err := aeadFromKey(encKey)
	if err != nil {
		return nil, err
	}
	return &Signer{repo: repo, aead: aead, now: time.Now}, nil
}

// SignJWT signs the supplied claims with the cached active Ed25519 key and embeds
// its kid in the protected header. Repository/decryption failures occur before a
// token is returned; claim maps are serialized as supplied by the provider.
func (s *Signer) SignJWT(ctx context.Context, claims map[string]any) (string, error) {
	k, err := s.activeKey(ctx)
	if err != nil {
		return "", err
	}
	header := map[string]any{"typ": "JWT", "alg": SigningAlg, "kid": k.kid}
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := b64(h) + "." + b64(p)
	sig := ed25519.Sign(k.key, []byte(unsigned))
	return unsigned + "." + b64(sig), nil
}

// JWKS publishes active, next, and retired public keys so tokens remain verifiable
// across rotation overlap. Private material is never decrypted or included by
// this path, and repository errors fail the entire response.
func (s *Signer) JWKS(ctx context.Context) ([]byte, error) {
	keys, err := s.repo.ListPublic(ctx)
	if err != nil {
		return nil, err
	}
	raw := make([]json.RawMessage, 0, len(keys))
	for _, k := range keys {
		if k.Status == KeyActive || k.Status == KeyNext || k.Status == KeyRetired {
			raw = append(raw, k.PublicJWK)
		}
	}
	return json.Marshal(map[string]any{"keys": raw})
}

func (s *Signer) activeKey(ctx context.Context) (*cachedKey, error) {
	s.mu.RLock()
	if s.cached != nil {
		defer s.mu.RUnlock()
		return s.cached, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil {
		return s.cached, nil
	}
	row, err := s.repo.GetActive(ctx)
	if err != nil {
		return nil, err
	}
	priv, err := decryptPrivateJWK(s.aead, row.PrivateEnc)
	if err != nil {
		return nil, err
	}
	s.cached = &cachedKey{kid: row.KID, key: priv}
	return s.cached, nil
}

// Invalidate clears the decrypted active-key cache after promotion or retirement.
// The mutex provides the handoff to concurrent signers; a subsequent SignJWT
// reloads the repository's current active key.
func (s *Signer) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cached = nil
}

// GenerateNextKey creates an encrypted Ed25519 key after verifying that at most
// one active and no next key exists. The first key becomes active immediately;
// later keys remain next until an explicit promotion.
func GenerateNextKey(ctx context.Context, repo SigningKeyRepo, encKey string, now int64) (string, error) {
	aead, err := aeadFromKey(encKey)
	if err != nil {
		return "", err
	}
	if active, err := repo.CountByStatus(ctx, KeyActive); err != nil {
		return "", err
	} else if active > 1 {
		return "", fmt.Errorf("oidp: expected at most one active signing key, found %d", active)
	}
	if next, err := repo.CountByStatus(ctx, KeyNext); err != nil {
		return "", err
	} else if next > 0 {
		return "", fmt.Errorf("oidp: next signing key already exists")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	kid := domain.NewULID()
	publicJWK, err := publicJWK(kid, pub)
	if err != nil {
		return "", err
	}
	privateEnc, err := encryptPrivateJWK(aead, kid, priv)
	if err != nil {
		return "", err
	}
	status := KeyNext
	if active, err := repo.CountByStatus(ctx, KeyActive); err != nil {
		return "", err
	} else if active == 0 {
		status = KeyActive
	}
	if err := repo.Create(ctx, domain.OAuthSigningKey{KID: kid, Alg: SigningAlg, PublicJWK: publicJWK, PrivateEnc: privateEnc, Status: status, CreatedAt: now}); err != nil {
		return "", err
	}
	return kid, nil
}

// PromoteNextKey delegates the transactional rotation to the repository, then
// verifies that exactly one active key remains. Callers must invalidate live
// Signer caches after a successful promotion.
// PromoteNextKey atomically asks the repository to make kid active after checking
// that exactly one active and one next key exist. Callers must invalidate live
// Signer caches after success so new tokens use the promoted key.
func PromoteNextKey(ctx context.Context, repo SigningKeyRepo, kid string, now int64) error {
	if err := repo.PromoteNext(ctx, kid, now); err != nil {
		return err
	}
	active, err := repo.CountByStatus(ctx, KeyActive)
	if err != nil {
		return err
	}
	if active != 1 {
		return fmt.Errorf("oidp: expected exactly one active signing key after promote, found %d", active)
	}
	return nil
}

// RetireKey removes a key from signing duty while retaining its public JWK for
// verification of unexpired tokens, then rechecks the single-active invariant.
// RetireKey removes a non-current key from active rotation while retaining its
// public JWK for verification. Repository constraints decide whether the requested
// transition is legal; callers invalidate signer caches after a relevant change.
func RetireKey(ctx context.Context, repo SigningKeyRepo, kid string, now int64) error {
	if err := repo.Retire(ctx, kid, now); err != nil {
		return err
	}
	active, err := repo.CountByStatus(ctx, KeyActive)
	if err != nil {
		return err
	}
	if active != 1 {
		return fmt.Errorf("oidp: expected exactly one active signing key after retire, found %d", active)
	}
	return nil
}

func publicJWK(kid string, pub ed25519.PublicKey) (json.RawMessage, error) {
	return json.Marshal(map[string]string{
		"kty": "OKP", "crv": "Ed25519", "alg": SigningAlg, "use": "sig", "kid": kid, "x": b64(pub),
	})
}

func encryptPrivateJWK(aead cipher.AEAD, kid string, priv ed25519.PrivateKey) ([]byte, error) {
	jwk, err := json.Marshal(map[string]string{
		"kty": "OKP", "crv": "Ed25519", "kid": kid, "d": b64(priv.Seed()), "x": b64(priv.Public().(ed25519.PublicKey)),
	})
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte(nil), nonce...)
	return aead.Seal(out, nonce, jwk, nil), nil
}

func decryptPrivateJWK(aead cipher.AEAD, enc []byte) (ed25519.PrivateKey, error) {
	if len(enc) < aead.NonceSize() {
		return nil, errors.New("oidp: encrypted private key too short")
	}
	nonce, ciphertext := enc[:aead.NonceSize()], enc[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("oidp: decrypt private key: %w", err)
	}
	var jwk struct {
		D string `json:"d"`
	}
	if err := json.Unmarshal(plain, &jwk); err != nil {
		return nil, err
	}
	seed, err := base64.RawURLEncoding.DecodeString(jwk.D)
	if err != nil {
		return nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("oidp: private key seed is %d bytes", len(seed))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func aeadFromKey(s string) (cipher.AEAD, error) {
	key, err := parseKey(s)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func parseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("oidp: OIDC_KEY_ENC_KEY is required")
	}
	for _, dec := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, base64.RawStdEncoding.DecodeString, base64.URLEncoding.DecodeString, base64.RawURLEncoding.DecodeString, hex.DecodeString} {
		if b, err := dec(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	if len(s) == 32 {
		return []byte(s), nil
	}
	return nil, errors.New("oidp: OIDC_KEY_ENC_KEY must decode to 32 bytes")
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

var _ SigningKeyRepo = (*store.OAuthSigningKeyRepo)(nil)
