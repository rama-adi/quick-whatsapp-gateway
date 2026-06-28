package assertion

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestAssertion_WrongAudienceRejected(t *testing.T) {
	m, v := newPair(t) // verifier expects aud=gw_1
	req := sampleRequest()
	req.Gateway = "gw_2" // minted for a different gateway
	tok, _ := m.Mint(samplePrincipal(), req)
	if _, err := v.Verify(context.Background(), tok, bindOf(req)); err == nil {
		t.Fatal("expected wrong-aud rejection")
	}
}

func TestAssertion_ExpiredRejected(t *testing.T) {
	past := func() time.Time { return time.Now().Add(-1 * time.Hour) }
	m, v := newPair(t, withMinterClock(past), WithTTL(30*time.Second))
	req := sampleRequest()
	tok, _ := m.Mint(samplePrincipal(), req)
	if _, err := v.Verify(context.Background(), tok, bindOf(req)); err == nil {
		t.Fatal("expected expired rejection")
	}
}

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

func TestAssertion_Middleware_SetsPrincipal(t *testing.T) {
	m, v := newPair(t)
	req := sampleRequest()
	tok, _ := m.Mint(samplePrincipal(), req)

	var gotPrincipal *authz.Principal
	h := Middleware(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrincipal = authz.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(req.Method, req.Path, strings.NewReader(string(req.Body)))
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
}

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
