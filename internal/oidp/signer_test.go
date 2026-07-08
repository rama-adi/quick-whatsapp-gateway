package oidp

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

type memKeys struct {
	keys map[string]domain.OAuthSigningKey
}

func newMemKeys() *memKeys { return &memKeys{keys: map[string]domain.OAuthSigningKey{}} }

func (m *memKeys) Create(_ context.Context, k domain.OAuthSigningKey) error {
	m.keys[k.KID] = k
	return nil
}
func (m *memKeys) GetActive(context.Context) (domain.OAuthSigningKey, error) {
	for _, k := range m.keys {
		if k.Status == KeyActive {
			return k, nil
		}
	}
	return domain.OAuthSigningKey{}, domain.ErrNotFound("oauth signing key not found")
}
func (m *memKeys) ListPublic(context.Context) ([]domain.OAuthSigningKey, error) {
	out := make([]domain.OAuthSigningKey, 0, len(m.keys))
	for _, k := range m.keys {
		out = append(out, k)
	}
	return out, nil
}
func (m *memKeys) CountByStatus(_ context.Context, status string) (int, error) {
	n := 0
	for _, k := range m.keys {
		if k.Status == status {
			n++
		}
	}
	return n, nil
}
func (m *memKeys) PromoteNext(_ context.Context, kid string, retiredAt int64) error {
	for id, k := range m.keys {
		if k.Status == KeyActive {
			k.Status, k.RetiredAt = KeyRetired, &retiredAt
			m.keys[id] = k
		}
	}
	k, ok := m.keys[kid]
	if !ok || k.Status != KeyNext {
		return domain.ErrNotFound("oauth signing key not found")
	}
	k.Status = KeyActive
	m.keys[kid] = k
	return nil
}
func (m *memKeys) Retire(_ context.Context, kid string, retiredAt int64) error {
	k, ok := m.keys[kid]
	if !ok {
		return domain.ErrNotFound("oauth signing key not found")
	}
	k.Status, k.RetiredAt = KeyRetired, &retiredAt
	m.keys[kid] = k
	return nil
}

func TestSigner_SignedJWTVerifiesAgainstJWKS(t *testing.T) {
	ctx := context.Background()
	repo := newMemKeys()
	encKey := base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	kid, err := GenerateNextKey(ctx, repo, encKey, 100)
	if err != nil {
		t.Fatalf("GenerateNextKey: %v", err)
	}
	signer, err := NewSigner(repo, encKey)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	token, err := signer.SignJWT(ctx, map[string]any{"iss": "https://issuer.test", "sub": "sub_1", "aud": "client_1"})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	jwks, err := signer.JWKS(ctx)
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	if !verifyJWTWithJWKS(t, token, jwks, kid) {
		t.Fatal("JWT did not verify against JWKS")
	}
}

func TestRotation_ExactlyOneActive(t *testing.T) {
	ctx := context.Background()
	repo := newMemKeys()
	encKey := base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	first, err := GenerateNextKey(ctx, repo, encKey, 100)
	if err != nil {
		t.Fatalf("Generate first: %v", err)
	}
	if first == "" {
		t.Fatal("empty first kid")
	}
	next, err := GenerateNextKey(ctx, repo, encKey, 200)
	if err != nil {
		t.Fatalf("Generate next: %v", err)
	}
	if err := PromoteNextKey(ctx, repo, next, 300); err != nil {
		t.Fatalf("PromoteNextKey: %v", err)
	}
	active, _ := repo.CountByStatus(ctx, KeyActive)
	if active != 1 {
		t.Fatalf("active count = %d, want 1", active)
	}
	if repo.keys[first].Status != KeyRetired || repo.keys[next].Status != KeyActive {
		t.Fatalf("unexpected states: %+v", repo.keys)
	}
}

func verifyJWTWithJWKS(t *testing.T, token string, jwks []byte, wantKID string) bool {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
	var set struct {
		Keys []struct {
			KID string `json:"kid"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(jwks, &set); err != nil {
		t.Fatalf("unmarshal jwks: %v", err)
	}
	for _, k := range set.Keys {
		if k.KID != wantKID {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			t.Fatalf("decode x: %v", err)
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			t.Fatalf("decode sig: %v", err)
		}
		return ed25519.Verify(ed25519.PublicKey(x), []byte(parts[0]+"."+parts[1]), sig)
	}
	t.Fatalf("kid %q not found in JWKS", wantKID)
	return false
}
