package authz

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

const (
	testIssuer = "https://auth.example.test"
	testKID    = "test-key-1"
)

// signer mints JWTs and exposes the matching public JWKS. It is the gateway side
// of the trust-seam contract: the frontend's better-auth jwt plugin is the real
// signer; here a test Ed25519 keypair stands in for it.
type signer struct {
	priv   jwk.Key // private key, with kid + alg set
	pubSet jwk.Set // public JWKS as served at /api/auth/jwks
	alg    jwa.SignatureAlgorithm
}

func newEd25519Signer(t *testing.T) *signer {
	t.Helper()
	return newSignerWithKID(t, testKID)
}

func newSignerWithKID(t *testing.T, kid string) *signer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen ed25519: %v", err)
	}
	return newSigner(t, priv, pub, jwa.EdDSA(), kid)
}

func newSigner(t *testing.T, rawPriv, rawPub any, alg jwa.SignatureAlgorithm, kid string) *signer {
	t.Helper()
	privKey, err := jwk.Import(rawPriv)
	if err != nil {
		t.Fatalf("import priv: %v", err)
	}
	_ = privKey.Set(jwk.KeyIDKey, kid)
	_ = privKey.Set(jwk.AlgorithmKey, alg)

	pubKey, err := jwk.Import(rawPub)
	if err != nil {
		t.Fatalf("import pub: %v", err)
	}
	_ = pubKey.Set(jwk.KeyIDKey, kid)
	_ = pubKey.Set(jwk.AlgorithmKey, alg)

	set := jwk.NewSet()
	_ = set.AddKey(pubKey)
	return &signer{priv: privKey, pubSet: set, alg: alg}
}

// mint builds and signs a JWT with the given claims.
func (s *signer) mint(t *testing.T, iss, aud, sub string, exp time.Time, claims map[string]any) string {
	t.Helper()
	b := jwt.NewBuilder().
		Issuer(iss).
		Audience([]string{aud}).
		Subject(sub).
		IssuedAt(time.Now()).
		Expiration(exp)
	for k, v := range claims {
		b = b.Claim(k, v)
	}
	tok, err := b.Build()
	if err != nil {
		t.Fatalf("build token: %v", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(s.alg, s.priv))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return string(signed)
}

// fetcherFor returns a jwksFetcher backed by the signer's public set, counting
// fetches so refresh behavior can be asserted.
func (s *signer) fetcherFor(count *int32) jwksFetcher {
	return func(_ context.Context, _ string) (jwk.Set, error) {
		atomic.AddInt32(count, 1)
		return s.pubSet, nil
	}
}

// TestJWTVerifier_VerifyToken table-tests signature, issuer, audience, expiry, and claim extraction.
// It supplies controlled credentials or repository results and observes the resolved principal or denial.
// This protects the caller-authentication boundary from fail-open behavior and upstream contract drift.
func TestJWTVerifier_VerifyToken(t *testing.T) {
	s := newEd25519Signer(t)
	goodClaims := map[string]any{
		claimActiveOrg: "org_123",
		claimOrgRole:   OrgRoleAdmin,
		claimRole:      PlatformRoleSuperAdmin,
	}

	tests := []struct {
		name    string
		token   func() string
		wantErr bool
		check   func(t *testing.T, p *Principal)
	}{
		{
			name: "valid token resolves principal",
			token: func() string {
				return s.mint(t, testIssuer, testIssuer, "user_1", time.Now().Add(time.Minute), goodClaims)
			},
			check: func(t *testing.T, p *Principal) {
				if p.Kind != KindUser {
					t.Errorf("kind = %v, want user", p.Kind)
				}
				if p.UserID != "user_1" {
					t.Errorf("UserID = %q, want user_1", p.UserID)
				}
				if p.OrganizationID != "org_123" {
					t.Errorf("OrganizationID = %q, want org_123", p.OrganizationID)
				}
				if p.OrgRole != OrgRoleAdmin {
					t.Errorf("OrgRole = %q, want admin", p.OrgRole)
				}
				if !p.IsSuperAdmin() {
					t.Errorf("IsSuperAdmin = false, want true")
				}
			},
		},
		{
			name: "valid token without org claims",
			token: func() string {
				return s.mint(t, testIssuer, testIssuer, "user_2", time.Now().Add(time.Minute), nil)
			},
			check: func(t *testing.T, p *Principal) {
				if p.UserID != "user_2" || p.OrganizationID != "" || p.OrgRole != "" {
					t.Errorf("unexpected principal %+v", p)
				}
			},
		},
		{
			name: "wrong issuer rejected",
			token: func() string {
				return s.mint(t, "https://evil.example", testIssuer, "user_1", time.Now().Add(time.Minute), goodClaims)
			},
			wantErr: true,
		},
		{
			name: "wrong audience rejected",
			token: func() string {
				return s.mint(t, testIssuer, "https://other.example", "user_1", time.Now().Add(time.Minute), goodClaims)
			},
			wantErr: true,
		},
		{
			name: "expired token rejected",
			token: func() string {
				return s.mint(t, testIssuer, testIssuer, "user_1", time.Now().Add(-time.Minute), goodClaims)
			},
			wantErr: true,
		},
		{
			name: "bad signature rejected",
			token: func() string {
				other := newEd25519Signer(t)
				return other.mint(t, testIssuer, testIssuer, "user_1", time.Now().Add(time.Minute), goodClaims)
			},
			wantErr: true,
		},
		{
			name:    "garbage token rejected",
			token:   func() string { return "not-a-jwt" },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fetches int32
			v, err := NewJWTVerifier(testIssuer+"/api/auth/jwks", testIssuer, withFetcher(s.fetcherFor(&fetches)))
			if err != nil {
				t.Fatalf("new verifier: %v", err)
			}
			p, err := v.VerifyToken(context.Background(), tt.token())
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got principal %+v", p)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyToken: %v", err)
			}
			if tt.check != nil {
				tt.check(t, p)
			}
		})
	}
}

// TestJWTVerifier_ServesOverHTTP exercises the real HTTP fetch path against an
// httptest JWKS endpoint — the end-to-end gateway side of the contract.
// It supplies controlled credentials or repository results and observes the resolved principal or denial.
// This protects the caller-authentication boundary from fail-open behavior and upstream contract drift.
func TestJWTVerifier_ServesOverHTTP(t *testing.T) {
	s := newEd25519Signer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		buf, err := json.Marshal(s.pubSet)
		if err != nil {
			t.Errorf("marshal jwks: %v", err)
			return
		}
		_, _ = w.Write(buf)
	}))
	defer srv.Close()

	v, err := NewJWTVerifier(srv.URL, testIssuer)
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	tok := s.mint(t, testIssuer, testIssuer, "user_http", time.Now().Add(time.Minute), nil)
	p, err := v.VerifyToken(context.Background(), tok)
	if err != nil {
		t.Fatalf("VerifyToken over http: %v", err)
	}
	if p.UserID != "user_http" {
		t.Errorf("UserID = %q, want user_http", p.UserID)
	}
}

// TestJWTVerifier_RefreshOnUnknownKID asserts that a token signed by a rotated
// key (a kid not in the cached set) triggers exactly one extra JWKS fetch and
// then verifies — the live key-rotation path of §4.1.
// It supplies controlled credentials or repository results and observes the resolved principal or denial.
// This protects the caller-authentication boundary from fail-open behavior and upstream contract drift.
func TestJWTVerifier_RefreshOnUnknownKID(t *testing.T) {
	first := newEd25519Signer(t) // initial key, kid = testKID
	second := newSignerWithKID(t, "rotated-kid")

	// The fetcher serves the FIRST key set until rotate is flipped, then the
	// SECOND — modeling the frontend rotating its signing key.
	var fetches int32
	var rotated atomic.Bool
	fetch := func(_ context.Context, _ string) (jwk.Set, error) {
		atomic.AddInt32(&fetches, 1)
		if rotated.Load() {
			return second.pubSet, nil
		}
		return first.pubSet, nil
	}

	v, err := NewJWTVerifier(testIssuer+"/api/auth/jwks", testIssuer,
		withFetcher(fetch),
		WithMinRefreshInterval(0),
	)
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}

	tok1 := first.mint(t, testIssuer, testIssuer, "u1", time.Now().Add(time.Minute), nil)
	if _, err := v.VerifyToken(context.Background(), tok1); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	afterFirst := atomic.LoadInt32(&fetches)
	if afterFirst == 0 {
		t.Fatal("expected a fetch on first verify")
	}

	// Frontend rotates its key; a new token carries the unknown kid.
	rotated.Store(true)
	tok2 := second.mint(t, testIssuer, testIssuer, "u2", time.Now().Add(time.Minute), nil)
	p, err := v.VerifyToken(context.Background(), tok2)
	if err != nil {
		t.Fatalf("verify after rotation: %v", err)
	}
	if p.UserID != "u2" {
		t.Errorf("UserID = %q, want u2", p.UserID)
	}
	if atomic.LoadInt32(&fetches) <= afterFirst {
		t.Errorf("expected a refresh fetch on unknown kid, fetches=%d (was %d)", fetches, afterFirst)
	}
}

// TestJWTVerifier_CoalescesConcurrentInitialFetch verifies concurrent refresh work is coalesced to prevent backend stampedes.
// It supplies controlled credentials or repository results and observes the resolved principal or denial.
// This protects the caller-authentication boundary from fail-open behavior and upstream contract drift.
func TestJWTVerifier_CoalescesConcurrentInitialFetch(t *testing.T) {
	s := newEd25519Signer(t)
	var calls atomic.Int32
	v, err := NewJWTVerifier(testIssuer+"/api/auth/jwks", testIssuer, withFetcher(func(context.Context, string) (jwk.Set, error) {
		calls.Add(1)
		return s.pubSet, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	tok := s.mint(t, testIssuer, testIssuer, "user_concurrent", time.Now().Add(time.Minute), nil)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.VerifyToken(context.Background(), tok); err != nil {
				t.Errorf("VerifyToken: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
}

// TestJWTVerifier_CanceledWaiterDoesNotBlockBehindRefresh verifies refresh serialization honors each caller's context.
// It blocks one cold-cache fetch, cancels a second verifier waiting for the refresh gate, and expects context.Canceled before releasing the fetch.
// This keeps authentication cancellation bounded even when another request owns slow JWKS I/O.
func TestJWTVerifier_CanceledWaiterDoesNotBlockBehindRefresh(t *testing.T) {
	s := newEd25519Signer(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	v, err := NewJWTVerifier(testIssuer+"/api/auth/jwks", testIssuer, withFetcher(func(context.Context, string) (jwk.Set, error) {
		close(entered)
		<-release
		return s.pubSet, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	tok := s.mint(t, testIssuer, testIssuer, "user_waiter", time.Now().Add(time.Minute), nil)
	first := make(chan error, 1)
	go func() {
		_, err := v.VerifyToken(context.Background(), tok)
		first <- err
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.VerifyToken(ctx, tok); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting VerifyToken error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first VerifyToken: %v", err)
	}
}

// TestJWTVerifier_RejectsEmptyJWKS verifies an empty trust store is an error, never anonymous trust.
// It supplies controlled credentials or repository results and observes the resolved principal or denial.
// This protects the caller-authentication boundary from fail-open behavior and upstream contract drift.
func TestJWTVerifier_RejectsEmptyJWKS(t *testing.T) {
	v, err := NewJWTVerifier(testIssuer+"/api/auth/jwks", testIssuer, withFetcher(func(context.Context, string) (jwk.Set, error) {
		return jwk.NewSet(), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.VerifyToken(context.Background(), "aaa.bbb.ccc"); err == nil {
		t.Fatal("expected an empty fetched JWKS to be rejected")
	}
}
