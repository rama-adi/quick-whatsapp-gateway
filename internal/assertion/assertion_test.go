package assertion

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

const (
	testIssuer  = "router"
	testGateway = "gw_1"
)

func newPair(t *testing.T, opts ...MinterOption) (*Minter, *Verifier) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMinter(priv, testIssuer, opts...)
	if err != nil {
		t.Fatalf("NewMinter: %v", err)
	}
	set, err := m.JWKS()
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	v, err := NewVerifier(StaticKeySet{Set: set}, testIssuer, testGateway)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return m, v
}

func sampleRequest() Request {
	return Request{
		Gateway: testGateway,
		Session: "wa_sess_1",
		Method:  http.MethodPost,
		Path:    "/api/v1/sessions/wa_sess_1/messages",
		Body:    []byte(`{"to":"123","text":"hi"}`),
	}
}

func samplePrincipal() Principal {
	return Principal{
		Kind:           "apikey",
		OrganizationID: "org_1",
		KeyID:          "key_1",
		Permissions:    domain.Permissions{Read: true, Send: true},
	}
}

func bindOf(req Request) Binding {
	return Binding{Method: req.Method, Path: req.Path, Body: req.Body}
}

type closeTrackingBody struct {
	*strings.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

// TestAssertion_MintVerify_RoundTrip verifies identity, permissions, and request binding survive signing.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestAssertion_MintVerify_RoundTrip(t *testing.T) {
	m, v := newPair(t)
	req := sampleRequest()
	tok, err := m.Mint(samplePrincipal(), req)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	p, err := v.Verify(context.Background(), tok, bindOf(req))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.OrganizationID != "org_1" || p.KeyID != "key_1" || p.Kind != "apikey" {
		t.Fatalf("principal not resolved: %+v", p)
	}
	if !p.Permissions.Read || !p.Permissions.Send || p.Permissions.Manage {
		t.Fatalf("permissions not round-tripped: %+v", p.Permissions)
	}
}

// TestAssertion_RejectsInvalidConstructionAndIdentity verifies unsafe options and partial identities fail closed.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestAssertion_RejectsInvalidConstructionAndIdentity(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	if _, err := NewMinter(priv, testIssuer, WithTTL(0)); err == nil {
		t.Fatal("expected zero assertion TTL to be rejected")
	}
	m, _ := newPair(t)
	if _, err := NewVerifier(StaticKeySet{Set: mustJWKS(t, m)}, testIssuer, testGateway, WithNonceCache(nil)); err == nil {
		t.Fatal("expected nil nonce cache to be rejected")
	}
	if _, err := m.Mint(Principal{Kind: "apikey", OrganizationID: "org_1"}, sampleRequest()); err == nil {
		t.Fatal("expected api-key principal without key id to be rejected")
	}
	if _, err := m.Mint(Principal{Kind: "user"}, sampleRequest()); err == nil {
		t.Fatal("expected user principal without user id to be rejected")
	}
}

func mustJWKS(t *testing.T, m *Minter) jwk.Set {
	t.Helper()
	set, err := m.JWKS()
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// TestRemoteKeySetCoalescesConcurrentInitialFetch verifies concurrent refresh work is coalesced to prevent backend stampedes.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestRemoteKeySetCoalescesConcurrentInitialFetch(t *testing.T) {
	m, _ := newPair(t)
	set := mustJWKS(t, m)
	var calls atomic.Int32
	rks, err := NewRemoteKeySet("https://router.test/jwks", withFetcher(func(context.Context, string) (jwk.Set, error) {
		calls.Add(1)
		return set, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := rks.KeySet(context.Background()); err != nil {
				t.Errorf("KeySet: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
}

// TestRemoteKeySetCanceledWaiterDoesNotBlockBehindRefresh verifies lock waiting remains context-aware.
// It holds the first JWKS fetch open, cancels a second caller waiting on the refresh gate, and expects an immediate cancellation error.
// This prevents a slow peer request from extending an already-canceled gateway request's lifetime.
func TestRemoteKeySetCanceledWaiterDoesNotBlockBehindRefresh(t *testing.T) {
	m, _ := newPair(t)
	set := mustJWKS(t, m)
	entered := make(chan struct{})
	release := make(chan struct{})
	rks, err := NewRemoteKeySet("https://router.test/jwks", withFetcher(func(context.Context, string) (jwk.Set, error) {
		close(entered)
		<-release
		return set, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() {
		_, err := rks.KeySet(context.Background())
		first <- err
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rks.KeySet(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting KeySet error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first KeySet: %v", err)
	}
}

// TestAssertion_ReplayRejected verifies a valid assertion nonce can be redeemed only once.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestAssertion_ReplayRejected(t *testing.T) {
	m, v := newPair(t)
	req := sampleRequest()
	tok, _ := m.Mint(samplePrincipal(), req)
	if _, err := v.Verify(context.Background(), tok, bindOf(req)); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if _, err := v.Verify(context.Background(), tok, bindOf(req)); err == nil {
		t.Fatal("expected replay rejection on second verify")
	}
}

// TestAssertion_TamperedBindingRejected verifies method, target, and body changes invalidate a captured assertion.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestAssertion_TamperedBindingRejected(t *testing.T) {
	req := sampleRequest()
	cases := map[string]Binding{
		"method": {Method: http.MethodGet, Path: req.Path, Body: req.Body},
		"path":   {Method: req.Method, Path: "/api/v1/sessions/other/messages", Body: req.Body},
		"body":   {Method: req.Method, Path: req.Path, Body: []byte(`{"to":"123","text":"BYE"}`)},
	}
	for name, bind := range cases {
		// Fresh pair each case so a burned nonce from another case can't mask a
		// missed binding check.
		m, v := newPair(t)
		tok, _ := m.Mint(samplePrincipal(), req)
		if _, err := v.Verify(context.Background(), tok, bind); err == nil {
			t.Fatalf("%s tamper: expected rejection", name)
		}
	}
}

// TestAssertion_WrongAudienceRejected verifies an assertion minted for one gateway cannot authorize another.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestAssertion_WrongAudienceRejected(t *testing.T) {
	m, v := newPair(t) // verifier expects aud=gw_1
	req := sampleRequest()
	req.Gateway = "gw_2" // minted for a different gateway
	tok, _ := m.Mint(samplePrincipal(), req)
	if _, err := v.Verify(context.Background(), tok, bindOf(req)); err == nil {
		t.Fatal("expected wrong-aud rejection")
	}
}

// TestAssertion_ExpiredRejected verifies stale assertions cannot outlive their short trust window.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestAssertion_ExpiredRejected(t *testing.T) {
	past := func() time.Time { return time.Now().Add(-1 * time.Hour) }
	m, v := newPair(t, withMinterClock(past), WithTTL(30*time.Second))
	req := sampleRequest()
	tok, _ := m.Mint(samplePrincipal(), req)
	if _, err := v.Verify(context.Background(), tok, bindOf(req)); err == nil {
		t.Fatal("expected expired rejection")
	}
}

// TestAssertion_WrongIssuerRejected verifies signatures from an unexpected router identity are rejected.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestAssertion_WrongIssuerRejected(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	m, _ := NewMinter(priv, "evil-router")
	set, _ := m.JWKS()
	v, _ := NewVerifier(StaticKeySet{Set: set}, testIssuer, testGateway) // expects iss=router
	req := sampleRequest()
	tok, _ := m.Mint(samplePrincipal(), req)
	if _, err := v.Verify(context.Background(), tok, bindOf(req)); err == nil {
		t.Fatal("expected wrong-issuer rejection")
	}
}

// TestAssertion_ForeignKeyRejected verifies a compromised gateway cannot forge router assertions.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestAssertion_ForeignKeyRejected(t *testing.T) {
	// A token signed by a different key (a compromised gateway trying to forge)
	// must not verify against the router's published JWKS.
	mGood, v := newPair(t)
	_ = mGood
	_, evilPriv, _ := ed25519.GenerateKey(nil)
	evil, _ := NewMinter(evilPriv, testIssuer)
	req := sampleRequest()
	tok, _ := evil.Mint(samplePrincipal(), req)
	if _, err := v.Verify(context.Background(), tok, bindOf(req)); err == nil {
		t.Fatal("expected foreign-key rejection")
	}
}

// TestAssertion_RemoteKeySet verifies gateway verification works through the production HTTP JWKS source.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestAssertion_RemoteKeySet(t *testing.T) {
	m, _ := newPair(t)
	set, _ := m.JWKS()
	buf, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buf)
	}))
	defer srv.Close()

	rks, err := NewRemoteKeySet(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(rks, testIssuer, testGateway)
	if err != nil {
		t.Fatal(err)
	}
	req := sampleRequest()
	tok, _ := m.Mint(samplePrincipal(), req)
	if _, err := v.Verify(context.Background(), tok, bindOf(req)); err != nil {
		t.Fatalf("remote-keyset verify: %v", err)
	}
}

// TestAssertion_Middleware_SetsPrincipal verifies trusted claims become auth context without consuming the body.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestAssertion_Middleware_SetsPrincipal(t *testing.T) {
	m, v := newPair(t)
	req := sampleRequest()
	tok, _ := m.Mint(samplePrincipal(), req)

	var gotPrincipal *authz.Principal
	h := Middleware(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrincipal = authz.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(req.Method, req.Path, nil)
	body := &closeTrackingBody{Reader: strings.NewReader(string(req.Body))}
	r.Body = body
	r.Header.Set(HeaderAssertion, tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPrincipal == nil || gotPrincipal.OrganizationID != "org_1" || gotPrincipal.Kind != authz.KindAPIKey {
		t.Fatalf("principal not set on context: %+v", gotPrincipal)
	}
	if !authz.Allow(gotPrincipal, authz.CapSend) {
		t.Fatal("expected send capability to be allowed")
	}
	if !body.closed {
		t.Fatal("assertion middleware did not close the original request body")
	}
}

// TestAssertion_Middleware_MissingAssertion verifies direct gateway calls without router trust are rejected.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestAssertion_Middleware_MissingAssertion(t *testing.T) {
	_, v := newPair(t)
	h := Middleware(v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not run without an assertion")
	}))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestAssertion_Middleware_ReadFailureClosesBody verifies ownership on the middleware error path.
// It supplies an unreadable body with an assertion header and expects a validation response without invoking the downstream handler.
// Closing the original body is required to release the server-side request stream after buffering fails.
func TestAssertion_Middleware_ReadFailureClosesBody(t *testing.T) {
	_, v := newPair(t)
	body := &failingBody{}
	h := Middleware(v)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run after a body read failure")
	}))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	r.Body = body
	r.Header.Set(HeaderAssertion, "present")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !body.closed {
		t.Fatal("unreadable original body was not closed")
	}
}

type failingBody struct{ closed bool }

func (b *failingBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (b *failingBody) Close() error {
	b.closed = true
	return nil
}

// TestParsePrivateKey_SeedAndFull verifies both documented Ed25519 encodings derive the same key ID.
// It uses controlled signing keys, claims, and request bindings to exercise the router-to-gateway trust seam.
// This prevents replay, forgery, key-rotation, and resource-lifecycle regressions from becoming silent authorization flaws.
func TestParsePrivateKey_SeedAndFull(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	wantKID := KeyID(pub)

	// Seed form (32 bytes).
	seed := priv.Seed()
	p1, kid1, err := ParsePrivateKey(b64url(seed))
	if err != nil || kid1 != wantKID || !p1.Equal(priv) {
		t.Fatalf("seed parse: kid=%q err=%v equal=%v", kid1, err, p1.Equal(priv))
	}
	// Full form (64 bytes).
	p2, kid2, err := ParsePrivateKey(b64url(priv))
	if err != nil || kid2 != wantKID || !p2.Equal(priv) {
		t.Fatalf("full parse: kid=%q err=%v", kid2, err)
	}
}
