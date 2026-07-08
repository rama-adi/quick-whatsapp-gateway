package router

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/assertion"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/oidp"
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

type fakeGateways struct {
	byID   map[string]domain.Gateway
	pick   *domain.Gateway
	active []domain.Gateway
}

func (f fakeGateways) Get(_ context.Context, id string) (domain.Gateway, error) {
	g, ok := f.byID[id]
	if !ok {
		return domain.Gateway{}, domain.ErrNotFound("gateway not found")
	}
	return g, nil
}
func (f fakeGateways) PickForPlacement(context.Context) (domain.Gateway, error) {
	if f.pick == nil {
		return domain.Gateway{}, domain.ErrNotFound("no placement gateway")
	}
	return *f.pick, nil
}
func (f fakeGateways) ListActive(context.Context) ([]domain.Gateway, error) {
	return f.active, nil
}

type fakeKeys struct{ p *authz.Principal }

func (f fakeKeys) VerifyKey(_ context.Context, raw string) (*authz.Principal, error) {
	if raw == "good-key" {
		return f.p, nil
	}
	return nil, errors.New("bad key")
}

// upstreamGateway is a fake gateway that verifies the router's assertion exactly
// as the real gateway middleware does, and records what it saw.
type upstreamGateway struct {
	srv      *httptest.Server
	verifier *assertion.Verifier
	gotAuth  string
	gotAssn  bool
	gotOrg   string
	body     string
}

func newUpstream(t *testing.T, m *assertion.Minter, gatewayID string) *upstreamGateway {
	t.Helper()
	set, err := m.JWKS()
	if err != nil {
		t.Fatal(err)
	}
	v, err := assertion.NewVerifier(assertion.StaticKeySet{Set: set}, "router", gatewayID)
	if err != nil {
		t.Fatal(err)
	}
	u := &upstreamGateway{verifier: v}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.gotAuth = r.Header.Get("Authorization")
		raw := r.Header.Get(assertion.HeaderAssertion)
		u.gotAssn = raw != ""
		body, _ := io.ReadAll(r.Body)
		u.body = string(body)
		p, err := v.Verify(r.Context(), raw, assertion.Binding{
			Method: r.Method, Path: assertion.RequestTarget(r), Body: body,
		})
		if err != nil {
			http.Error(w, "assertion verify failed: "+err.Error(), http.StatusUnauthorized)
			return
		}
		u.gotOrg = p.OrganizationID
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream-ok"))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// --- helpers -----------------------------------------------------------------

func newTestServer(t *testing.T, sessions fakeSessions, gateways fakeGateways) (*Server, *assertion.Minter) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	m, err := assertion.NewMinter(priv, "router")
	if err != nil {
		t.Fatal(err)
	}
	principal := &authz.Principal{Kind: authz.KindAPIKey, OrganizationID: "org_1",
		KeyID: "k1", KeyPermissions: domain.Permissions{Read: true, Send: true, Manage: true}}
	srv, err := NewServer(Config{
		Sessions: sessions, Gateways: gateways, Minter: m,
		Keys: fakeKeys{p: principal},
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, m
}

func activeGateway(id, baseURL string) domain.Gateway {
	now := domain.NowMs()
	return domain.Gateway{ID: id, Status: domain.GatewayActive, BaseURL: &baseURL, LastSeenAt: &now}
}

// --- tests -------------------------------------------------------------------

func TestSessionFromPath(t *testing.T) {
	cases := map[string]string{
		"/api/v1/sessions":                        "",
		"/api/v1/sessions/wa_1":                   "wa_1",
		"/api/v1/sessions/wa_1:start":             "wa_1",
		"/api/v1/sessions/wa_1/messages":          "wa_1",
		"/api/v1/admin/sessions/wa_2:backfill":    "wa_2",
		"/api/v1/admin/sessions":                  "",
		"/api/v1/webhooks":                        "",
		"/api/v1/sessions/wa_3/chats/c1/messages": "wa_3",
	}
	for path, want := range cases {
		if got := sessionFromPath(path); got != want {
			t.Errorf("sessionFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

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
	srv, _ := newTestServer(t, fakeSessions{}, fakeGateways{})
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

func TestBroker_SessionScoped_ProxiesUnderAssertion(t *testing.T) {
	gatewayID := "gw_1"
	sessions := fakeSessions{m: map[string]domain.WASession{
		"wa_1": {ID: "wa_1", OrganizationID: "org_1", GatewayID: gatewayID},
	}}
	srv, m := newTestServer(t, sessions, fakeGateways{})
	up := newUpstream(t, m, gatewayID)
	// Point the resolver at the upstream now that we know its URL.
	srv.gateways = fakeGateways{byID: map[string]domain.Gateway{gatewayID: activeGateway(gatewayID, up.srv.URL)}}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/wa_1/chats", nil)
	r.Header.Set("X-Api-Key", "good-key")
	r.Header.Set("Authorization", "Bearer should-be-stripped")
	srv.Handler().ServeHTTP(rec, r)

	if rec.Code != http.StatusOK || rec.Body.String() != "upstream-ok" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if !up.gotAssn {
		t.Fatal("upstream did not receive an internal assertion")
	}
	if up.gotAuth != "" {
		t.Fatalf("inbound Authorization was forwarded: %q", up.gotAuth)
	}
	if up.gotOrg != "org_1" {
		t.Fatalf("asserted org = %q, want org_1", up.gotOrg)
	}
}

func TestBroker_PostBodyBoundIntoAssertion(t *testing.T) {
	gatewayID := "gw_1"
	sessions := fakeSessions{m: map[string]domain.WASession{
		"wa_1": {ID: "wa_1", OrganizationID: "org_1", GatewayID: gatewayID},
	}}
	srv, m := newTestServer(t, sessions, fakeGateways{})
	up := newUpstream(t, m, gatewayID)
	srv.gateways = fakeGateways{byID: map[string]domain.Gateway{gatewayID: activeGateway(gatewayID, up.srv.URL)}}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/wa_1/messages",
		strings.NewReader(`{"to":"123","text":"hi"}`))
	r.Header.Set("X-Api-Key", "good-key")
	srv.Handler().ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if up.body != `{"to":"123","text":"hi"}` {
		t.Fatalf("upstream body = %q", up.body)
	}
}

func TestBroker_OrgIsolation_404(t *testing.T) {
	gatewayID := "gw_1"
	sessions := fakeSessions{m: map[string]domain.WASession{
		"wa_other": {ID: "wa_other", OrganizationID: "org_2", GatewayID: gatewayID}, // different org
	}}
	srv, _ := newTestServer(t, sessions, fakeGateways{
		byID: map[string]domain.Gateway{gatewayID: activeGateway(gatewayID, "http://unused")},
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/wa_other/chats", nil)
	r.Header.Set("X-Api-Key", "good-key")
	srv.Handler().ServeHTTP(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (org isolation)", rec.Code)
	}
}

func TestBroker_StrandedSession_503(t *testing.T) {
	gatewayID := "gw_dead"
	sessions := fakeSessions{m: map[string]domain.WASession{
		"wa_1": {ID: "wa_1", OrganizationID: "org_1", GatewayID: gatewayID},
	}}
	dead := activeGateway(gatewayID, "http://unused")
	dead.Status = domain.GatewayDrained // not usable
	srv, _ := newTestServer(t, sessions, fakeGateways{
		byID: map[string]domain.Gateway{gatewayID: dead},
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/wa_1/chats", nil)
	r.Header.Set("X-Api-Key", "good-key")
	srv.Handler().ServeHTTP(rec, r)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503 (stranded session)", rec.Code)
	}
}

func TestBroker_Placement_OnCreate(t *testing.T) {
	gatewayID := "gw_place"
	srv, m := newTestServer(t, fakeSessions{m: map[string]domain.WASession{}}, fakeGateways{})
	up := newUpstream(t, m, gatewayID)
	g := activeGateway(gatewayID, up.srv.URL)
	srv.gateways = fakeGateways{pick: &g}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"label":"x"}`))
	r.Header.Set("X-Api-Key", "good-key")
	srv.Handler().ServeHTTP(rec, r)

	if rec.Code != http.StatusOK || !up.gotAssn {
		t.Fatalf("placement failed: status=%d assn=%v body=%q", rec.Code, up.gotAssn, rec.Body.String())
	}
}

func TestBroker_AgnosticRoute_AnyActive(t *testing.T) {
	gatewayID := "gw_any"
	srv, m := newTestServer(t, fakeSessions{m: map[string]domain.WASession{}}, fakeGateways{})
	up := newUpstream(t, m, gatewayID)
	srv.gateways = fakeGateways{active: []domain.Gateway{activeGateway(gatewayID, up.srv.URL)}}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/webhooks", nil)
	r.Header.Set("X-Api-Key", "good-key")
	srv.Handler().ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("agnostic route status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestBroker_Unauthenticated_401(t *testing.T) {
	srv, _ := newTestServer(t, fakeSessions{m: map[string]domain.WASession{}}, fakeGateways{})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/wa_1/chats", nil)
	r.Header.Set("X-Api-Key", "bad")
	srv.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

func TestJWKSEndpoint(t *testing.T) {
	srv, _ := newTestServer(t, fakeSessions{m: map[string]domain.WASession{}}, fakeGateways{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, JWKSPath, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "keys") {
		t.Fatalf("jwks endpoint: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

// The default reverse-proxy transport must bound the wait for the upstream
// gateway's response headers (Fix 1, router side): without ResponseHeaderTimeout a
// wedged gateway would leave the client's connection open forever instead of
// surfacing the ErrorHandler's 503. This asserts the transport is configured; the
// 503 rendering on RoundTrip failure is covered by the ErrorHandler path.
func TestDefaultProxyTransportBoundsResponseHeaders(t *testing.T) {
	rt := defaultProxyTransport()
	tr, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("default proxy transport type = %T, want *http.Transport", rt)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Fatal("default proxy transport must set a positive ResponseHeaderTimeout so a hung gateway cannot hang the client")
	}
}
