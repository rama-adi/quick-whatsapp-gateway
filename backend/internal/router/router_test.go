package router

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/oidp"
)

// --- fakes -------------------------------------------------------------------

type fakeSessions struct{ m map[string]domain.WASession }

func (f fakeSessions) Get(_ context.Context, id string) (domain.WASession, error) {
	s, ok := f.m[id]
	if !ok {
		return domain.WASession{}, domain.ErrNotFound("session not found")
	}
	return s, nil
}

type fakeKeys struct{ p *authz.Principal }

func (f fakeKeys) VerifyKey(_ context.Context, raw string) (*authz.Principal, error) {
	if raw == "good-key" {
		return f.p, nil
	}
	return nil, errors.New("bad key")
}

type fakeTokens struct{ p *authz.Principal }

func (f fakeTokens) VerifyToken(_ context.Context, raw string) (*authz.Principal, error) {
	if raw == "aaa.bbb.ccc" {
		return f.p, nil
	}
	return nil, errors.New("bad token")
}

// --- helpers -----------------------------------------------------------------

func newTestServer(t *testing.T, sessions fakeSessions) *Server {
	t.Helper()
	principal := &authz.Principal{Kind: authz.KindAPIKey, OrganizationID: "org_1",
		KeyID: "k1", KeyPermissions: domain.Permissions{Read: true, Send: true, Manage: true, Events: true}}
	srv, err := NewServer(Config{
		Sessions: sessions,
		Tokens:   fakeTokens{p: principal},
		Keys:     fakeKeys{p: principal},
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// --- tests -------------------------------------------------------------------

// TestOIDCWellKnownRoutes verifies the OIDC discovery + JWKS endpoints serve a
// consistent issuer and verifiable signing keys.
func TestOIDCWellKnownRoutes(t *testing.T) {
	repo := oidpTestKeys(t)
	encKey := base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	kid, err := oidp.GenerateNextKey(context.Background(), repo, encKey, 100)
	if err != nil {
		t.Fatalf("GenerateNextKey: %v", err)
	}
	signer, err := oidp.NewSigner(repo, encKey)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	srv := newTestServer(t, fakeSessions{})
	srv.oidcIssuer = "https://issuer.test"
	srv.oidpSigner = signer
	h := srv.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("discovery status = %d body=%s", rr.Code, rr.Body.String())
	}
	var discovery map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &discovery); err != nil {
		t.Fatalf("discovery json: %v", err)
	}
	if discovery["issuer"] != "https://issuer.test" || discovery["jwks_uri"] != "https://issuer.test/.well-known/oauth-jwks.json" {
		t.Fatalf("unexpected discovery: %+v", discovery)
	}

	token, err := signer.SignJWT(context.Background(), map[string]any{"iss": "https://issuer.test", "sub": "sub_1"})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-jwks.json", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("jwks status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !oidpVerifyJWTForRouterTest(t, token, rr.Body.Bytes(), kid) {
		t.Fatal("signed JWT did not verify against endpoint JWKS")
	}
}

type oidpMemoryKeys struct {
	keys map[string]domain.OAuthSigningKey
}

func oidpTestKeys(t *testing.T) *oidpMemoryKeys {
	t.Helper()
	return &oidpMemoryKeys{keys: map[string]domain.OAuthSigningKey{}}
}

func (m *oidpMemoryKeys) Create(_ context.Context, k domain.OAuthSigningKey) error {
	m.keys[k.KID] = k
	return nil
}
func (m *oidpMemoryKeys) GetActive(context.Context) (domain.OAuthSigningKey, error) {
	for _, k := range m.keys {
		if k.Status == oidp.KeyActive {
			return k, nil
		}
	}
	return domain.OAuthSigningKey{}, domain.ErrNotFound("oauth signing key not found")
}
func (m *oidpMemoryKeys) ListPublic(context.Context) ([]domain.OAuthSigningKey, error) {
	out := make([]domain.OAuthSigningKey, 0, len(m.keys))
	for _, k := range m.keys {
		out = append(out, k)
	}
	return out, nil
}
func (m *oidpMemoryKeys) CountByStatus(_ context.Context, status string) (int, error) {
	n := 0
	for _, k := range m.keys {
		if k.Status == status {
			n++
		}
	}
	return n, nil
}
func (m *oidpMemoryKeys) PromoteNext(_ context.Context, kid string, retiredAt int64) error {
	for id, k := range m.keys {
		if k.Status == oidp.KeyActive {
			k.Status, k.RetiredAt = oidp.KeyRetired, &retiredAt
			m.keys[id] = k
		}
	}
	k := m.keys[kid]
	k.Status = oidp.KeyActive
	m.keys[kid] = k
	return nil
}
func (m *oidpMemoryKeys) Retire(_ context.Context, kid string, retiredAt int64) error {
	k := m.keys[kid]
	k.Status, k.RetiredAt = oidp.KeyRetired, &retiredAt
	m.keys[kid] = k
	return nil
}

func oidpVerifyJWTForRouterTest(t *testing.T, token string, jwks []byte, kid string) bool {
	t.Helper()
	parts := strings.Split(token, ".")
	var set struct {
		Keys []struct {
			KID string `json:"kid"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(jwks, &set); err != nil {
		t.Fatalf("jwks json: %v", err)
	}
	for _, k := range set.Keys {
		if k.KID != kid {
			continue
		}
		pub, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			t.Fatalf("decode jwk x: %v", err)
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			t.Fatalf("decode jwt sig: %v", err)
		}
		return ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig)
	}
	return false
}

// TestUnauthenticatedAPIRejected verifies unauthenticated callers are rejected
// with 401 before any protected operation runs.
func TestUnauthenticatedAPIRejected(t *testing.T) {
	srv := newTestServer(t, fakeSessions{m: map[string]domain.WASession{}})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/realtime/ticket", nil)
	r.Header.Set("X-Api-Key", "bad")
	srv.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}
