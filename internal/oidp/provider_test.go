package oidp

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

type fakeOAuthClients map[string]domain.OAuthClient

func (f fakeOAuthClients) GetActiveByClientID(_ context.Context, id string) (domain.OAuthClient, error) {
	c, ok := f[id]
	if !ok || c.Status != "active" {
		return domain.OAuthClient{}, domain.ErrNotFound("oauth client not found")
	}
	return c, nil
}

type fakeOIDPSessions map[string]domain.WASession

func (f fakeOIDPSessions) Get(_ context.Context, id string) (domain.WASession, error) {
	s, ok := f[id]
	if !ok {
		return domain.WASession{}, domain.ErrNotFound("session not found")
	}
	return s, nil
}

type fakeOIDPGroups map[string]domain.Group

func (f fakeOIDPGroups) GetByJID(_ context.Context, jid string) (domain.Group, error) {
	g, ok := f[jid]
	if !ok {
		return domain.Group{}, domain.ErrNotFound("group not found")
	}
	return g, nil
}

func testRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Skipf("miniredis unavailable in this sandbox: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rdb.Close()
		mr.Close()
	})
	return mr, rdb
}

func testHTTPServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Skipf("httptest server unavailable in this sandbox: %v", r)
		}
	}()
	return httptest.NewServer(h)
}

func testProvider(t *testing.T) (*Provider, *PendingStore) {
	t.Helper()
	_, rdb := testRedis(t)
	// Anchor the logical clock to real-now: Redis TTLs are honored by miniredis
	// against its real clock, so the provider's clock must agree with it.
	now := time.Now()
	client := domain.OAuthClient{
		ClientID: "client_1", OrganizationID: "org_1", SessionID: "sess_1", Name: "Acme",
		LoginCommand: "login", RedirectURIs: json.RawMessage(`["https://rp.example/cb"]`),
		Modes: "dm,group", GroupJID: strp("120@g.us"), AllowedScopes: json.RawMessage(`["openid","profile","phone","wa:group"]`),
		Status: "active",
	}
	pending := NewPendingStore(rdb, "test", 10*time.Minute)
	pending.SetClock(func() time.Time { return now })
	p := NewProvider(ProviderConfig{
		Clients:  fakeOAuthClients{"client_1": client},
		Sessions: fakeOIDPSessions{"sess_1": {ID: "sess_1", OrganizationID: "org_1", Status: domain.SessionWorking, PhoneNumber: strp("628123456789"), Label: strp("Bot")}},
		Groups:   fakeOIDPGroups{"120@g.us": {GroupJID: "120@g.us", Subject: strp("Members")}},
		Pending:  pending, WebLoginURL: "https://web.example/login/whatsapp", RequestTTL: 10 * time.Minute,
		Now: func() time.Time { return now },
	})
	return p, pending
}

func TestAuthorizeValidationMatrix(t *testing.T) {
	p, _ := testProvider(t)
	cases := []struct {
		name             string
		query            string
		want             int
		locationContains string
		bodyContains     string
	}{
		{"bad client local", "response_type=code&client_id=bad&redirect_uri=https://rp.example/cb&code_challenge=x&code_challenge_method=S256", http.StatusBadRequest, "", "invalid_client"},
		{"bad redirect local", "response_type=code&client_id=client_1&redirect_uri=https://evil.example/cb&code_challenge=x&code_challenge_method=S256", http.StatusBadRequest, "", "Invalid redirect_uri"},
		{"missing pkce redirects", "response_type=code&client_id=client_1&redirect_uri=https://rp.example/cb&state=s", http.StatusFound, "error=invalid_request", ""},
		{"plain pkce rejects", "response_type=code&client_id=client_1&redirect_uri=https://rp.example/cb&code_challenge=x&code_challenge_method=plain&state=s", http.StatusFound, "error=invalid_request", ""},
		{"scope subset rejects", "response_type=code&client_id=client_1&redirect_uri=https://rp.example/cb&code_challenge=x&code_challenge_method=S256&scope=openid+email", http.StatusFound, "error=invalid_scope", ""},
		{"acr resolves group", "response_type=code&client_id=client_1&redirect_uri=https://rp.example/cb&code_challenge=x&code_challenge_method=S256&scope=openid&acr_values=wa:group", http.StatusFound, "https://web.example/login/whatsapp#c=", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+tc.query, nil)
			p.HandleAuthorize(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			if tc.locationContains != "" && !strings.Contains(rec.Header().Get("Location"), tc.locationContains) {
				t.Fatalf("location %q does not contain %q", rec.Header().Get("Location"), tc.locationContains)
			}
			if tc.bodyContains != "" && !strings.Contains(rec.Body.String(), tc.bodyContains) {
				t.Fatalf("body %q does not contain %q", rec.Body.String(), tc.bodyContains)
			}
		})
	}
}

func TestCodeMinting(t *testing.T) {
	a, err := NewBrowserCode()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewBrowserCode()
	if len(a) != 27 || a == b || strings.ContainsAny(a, "+/=") {
		t.Fatalf("browser code shape/entropy bad: %q %q", a, b)
	}
	for _, code := range []string{"000000", "123456", "987654"} {
		if !patternedUserCode(code) {
			t.Fatalf("pattern %s not rejected", code)
		}
	}
	if patternedUserCode("483920") {
		t.Fatal("random-looking code rejected")
	}
}

func TestPendingCancelAndClaimState(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "test", 10*time.Minute)
	req := PendingRequest{ClientID: "c", BrowserCode: "b", SessionID: "s", UserCode: "483920", LoginCommand: "login", Mode: "dm", Status: PendingStatusPending, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
	if err := ps.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if ok, err := ps.Cancel(context.Background(), "b"); err != nil || !ok {
		t.Fatalf("cancel ok=%v err=%v", ok, err)
	}
	got, err := ps.Load(context.Background(), "b")
	if err != nil || got.Status != PendingStatusDenied {
		t.Fatalf("status=%q err=%v", got.Status, err)
	}
	if ok, err := ps.Cancel(context.Background(), "b"); err != nil || !ok {
		t.Fatalf("idempotent cancel ok=%v err=%v", ok, err)
	}
}

func TestWaitStreamSnapshotFrameShape(t *testing.T) {
	p, ps := testProvider(t)
	req := PendingRequest{
		ClientID: "client_1", BrowserCode: "browser", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", AppName: "Acme", AppLogo: strp("https://logo.example/a.png"),
		Target: PendingTarget{Mode: "dm", Number: strp("+628123456789"), BotName: strp("Bot")},
		Scopes: []string{"openid", "phone"}, Status: PendingStatusPending, ExpiresAt: time.Now().Add(100 * time.Millisecond).UnixMilli(),
	}
	if err := ps.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r := chi.NewRouter()
	p.Mount(r)
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/wait/browser/stream", nil))
	line, _ := bufio.NewReader(strings.NewReader(rec.Body.String())).ReadString('\n')
	var frame map[string]any
	if err := json.Unmarshal([]byte(line), &frame); err != nil {
		t.Fatalf("snapshot json: %v line=%q", err, line)
	}
	for _, k := range []string{"status", "app", "user_code", "login_command", "target", "scopes", "expires_at"} {
		if _, ok := frame[k]; !ok {
			t.Fatalf("missing key %q in %#v", k, frame)
		}
	}
	if frame["status"] != "pending" {
		t.Fatalf("status=%v", frame["status"])
	}
	target := frame["target"].(map[string]any)
	if target["mode"] != "dm" || target["number"] != "+628123456789" || target["bot_name"] != "Bot" {
		t.Fatalf("target=%#v", target)
	}
}

func TestWaitStreamUnknownCodeIsGeneric404(t *testing.T) {
	p, _ := testProvider(t)
	rec := httptest.NewRecorder()
	p.HandleWaitStream(rec, httptest.NewRequest(http.MethodGet, "/oauth/wait/missing/stream", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestWaitStreamConcurrentConnectionCaps(t *testing.T) {
	p, ps := testProvider(t)
	req := PendingRequest{
		ClientID: "client_1", BrowserCode: "browser", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}
	if err := ps.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	p.Mount(r)
	srv := testHTTPServer(t, r)
	t.Cleanup(srv.Close)

	resps := make([]*http.Response, 0, 3)
	for i := 0; i < 3; i++ {
		resp, err := http.Get(srv.URL + "/oauth/wait/browser/stream")
		if err != nil {
			t.Skipf("httptest stream unavailable in this sandbox: %v", err)
		}
		resps = append(resps, resp)
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		if err != nil || !strings.Contains(line, `"status":"pending"`) {
			t.Fatalf("stream %d first line=%q err=%v", i, line, err)
		}
	}
	t.Cleanup(func() {
		for _, resp := range resps {
			_ = resp.Body.Close()
		}
	})

	resp, err := http.Get(srv.URL + "/oauth/wait/browser/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%q", resp.StatusCode, string(body))
	}

	for i := 0; i < 30; i++ {
		code := "ipcap_" + strconv.Itoa(i)
		if !p.enterStream(code, "203.0.113.10") {
			t.Fatalf("unexpected per-IP rejection at %d", i)
		}
		defer p.leaveStream(code, "203.0.113.10")
	}
	if p.enterStream("ipcap_over", "203.0.113.10") {
		t.Fatal("per-IP cap accepted 31st stream")
	}
}

type fakeIdentities map[uint64]domain.Identity

func (f fakeIdentities) GetByLID(_ context.Context, lid string) (domain.Identity, error) {
	for _, ident := range f {
		if ident.LID == lid {
			return ident, nil
		}
	}
	return domain.Identity{}, domain.ErrNotFound("identity not found")
}
func (f fakeIdentities) GetByID(_ context.Context, id uint64) (domain.Identity, error) {
	ident, ok := f[id]
	if !ok {
		return domain.Identity{}, domain.ErrNotFound("identity not found")
	}
	return ident, nil
}

type memGrants struct {
	mu     sync.Mutex
	byID   map[string]domain.OAuthGrant
	byPair map[string]string
}

func newMemGrants() *memGrants {
	return &memGrants{byID: map[string]domain.OAuthGrant{}, byPair: map[string]string{}}
}
func (m *memGrants) UpsertAndGet(_ context.Context, g domain.OAuthGrant) (domain.OAuthGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := g.ClientID + "|" + strconv.FormatUint(g.WAIdentityID, 10)
	if id := m.byPair[key]; id != "" {
		g.ID = id
	}
	m.byID[g.ID] = g
	m.byPair[key] = g.ID
	return g, nil
}
func (m *memGrants) GetActiveByID(_ context.Context, id string) (domain.OAuthGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.byID[id]
	if !ok || g.RevokedAt != nil {
		return domain.OAuthGrant{}, domain.ErrNotFound("grant not found")
	}
	return g, nil
}
func (m *memGrants) put(g domain.OAuthGrant) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[g.ID] = g
}
func (m *memGrants) revoke(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.byID[id]
	now := int64(1)
	g.RevokedAt = &now
	m.byID[id] = g
}

type memRefresh struct {
	mu     sync.Mutex
	byID   map[string]domain.OAuthRefreshToken
	byHash map[string]string
}

func newMemRefresh() *memRefresh {
	return &memRefresh{byID: map[string]domain.OAuthRefreshToken{}, byHash: map[string]string{}}
}
func (m *memRefresh) Create(_ context.Context, rt domain.OAuthRefreshToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[rt.ID] = rt
	m.byHash[string(rt.TokenHash)] = rt.ID
	return nil
}
func (m *memRefresh) GetByHash(_ context.Context, h []byte) (domain.OAuthRefreshToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.byHash[string(h)]
	rt, ok := m.byID[id]
	if !ok {
		return domain.OAuthRefreshToken{}, domain.ErrNotFound("refresh not found")
	}
	return rt, nil
}
func (m *memRefresh) MarkConsumed(_ context.Context, id string, ts int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.byID[id]
	if !ok || rt.ConsumedAt != nil || rt.RevokedAt != nil {
		return domain.ErrNotFound("refresh not active")
	}
	rt.ConsumedAt = &ts
	m.byID[id] = rt
	return nil
}
func (m *memRefresh) RevokeFamily(_ context.Context, family string, ts int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, rt := range m.byID {
		if rt.FamilyID == family {
			rt.RevokedAt = &ts
			m.byID[id] = rt
		}
	}
	return nil
}
func (m *memRefresh) RevokeTokenHash(_ context.Context, h []byte, ts int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id := m.byHash[string(h)]; id != "" {
		rt := m.byID[id]
		rt.RevokedAt = &ts
		m.byID[id] = rt
	}
	return nil
}
func (m *memRefresh) RotateRefreshToken(_ context.Context, rot domain.OAuthRefreshRotation) (domain.OAuthRefreshToken, domain.OAuthGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.byHash[string(rot.TokenHash)]
	rt, ok := m.byID[id]
	if !ok || rt.ExpiresAt <= rot.Now || rt.RevokedAt != nil {
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, domain.ErrNotFound("refresh not found")
	}
	if rt.ConsumedAt != nil {
		for rid, item := range m.byID {
			if item.FamilyID == rt.FamilyID {
				item.RevokedAt = &rot.Now
				m.byID[rid] = item
			}
		}
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, domain.ErrNotFound("refresh reused")
	}
	oldScopes, _ := scopesFromRaw(rt.Scopes)
	nextScopes := oldScopes
	if len(rot.RequestedScopes) > 0 {
		if !scopeSubset(rot.RequestedScopes, oldScopes) {
			return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, domain.ErrOAuthScopeWidening
		}
		nextScopes = rot.RequestedScopes
	}
	rt.ConsumedAt = &rot.Now
	m.byID[id] = rt
	successor := rot.Successor
	successor.GrantID = rt.GrantID
	successor.OrganizationID = rt.OrganizationID
	successor.FamilyID = rt.FamilyID
	successor.ParentID = &rt.ID
	successor.Scopes, _ = json.Marshal(nextScopes)
	successor.IssuedAt = rot.Now
	successor.ExpiresAt = rt.ExpiresAt
	m.byID[successor.ID] = successor
	m.byHash[string(successor.TokenHash)] = successor.ID
	return rt, domain.OAuthGrant{ID: rt.GrantID, OrganizationID: rt.OrganizationID, ClientID: rot.ClientID, WAIdentityID: 42, Sub: "sub", LastACR: "wa:dm"}, nil
}
func (m *memRefresh) all() []domain.OAuthRefreshToken {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.OAuthRefreshToken, 0, len(m.byID))
	for _, rt := range m.byID {
		out = append(out, rt)
	}
	return out
}

func fullProvider(t *testing.T) (*Provider, *PendingStore, *memGrants, *memRefresh) {
	t.Helper()
	_, rdb := testRedis(t)
	// Anchor to real-now: miniredis honors Redis TTLs against its real clock, so
	// the provider/store logical clock must agree with it (auth-code PXAT etc.).
	now := time.Now()
	secret := "super-secret"
	clients := fakeOAuthClients{
		"client_1": oauthClient("client_1", "confidential", secret, "active"),
		"client_2": oauthClient("client_2", "confidential", secret, "active"),
		"public_1": oauthClient("public_1", "public", "", "active"),
	}
	clients["disabled"] = oauthClient("disabled", "confidential", secret, "disabled")
	keys := newMemKeys()
	encKey := base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	if _, err := GenerateNextKey(context.Background(), keys, encKey, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(keys, encKey)
	if err != nil {
		t.Fatal(err)
	}
	pending := NewPendingStore(rdb, "test", 10*time.Minute)
	pending.SetClock(func() time.Time { return now })
	grants, refresh := newMemGrants(), newMemRefresh()
	p := NewProvider(ProviderConfig{
		Clients:    clients,
		Sessions:   fakeOIDPSessions{"sess_1": {ID: "sess_1", OrganizationID: "org_1", Status: domain.SessionWorking, PhoneNumber: strp("628123456789"), Label: strp("Bot")}},
		Groups:     fakeOIDPGroups{"120@g.us": {GroupJID: "120@g.us", Subject: strp("Members")}},
		Identities: fakeIdentities{42: {ID: 42, LID: "42@lid", PhoneNumber: strp("+628111"), PhoneJID: strp("628111@s.whatsapp.net"), Name: strp("Alice")}},
		Grants:     grants, Refresh: refresh, Signer: signer, Pending: pending,
		WebLoginURL: "https://web.example/login/whatsapp", Issuer: "https://issuer.example",
		SecretPepper: "pepper", PairwiseSalt: "pairwise", RequestTTL: 10 * time.Minute, AuthCodeTTL: time.Minute,
		Now: func() time.Time { return now },
	})
	return p, pending, grants, refresh
}

func oauthClient(id, typ, secret, status string) domain.OAuthClient {
	return domain.OAuthClient{
		ClientID: id, OrganizationID: "org_1", SessionID: "sess_1", Name: "Acme", ClientType: typ,
		LoginCommand: "login", RedirectURIs: json.RawMessage(`["https://rp.example/cb"]`),
		Modes: "dm,group", GroupJID: strp("120@g.us"), AllowedScopes: json.RawMessage(`["openid","profile","phone","wa:group","offline_access"]`),
		TokenTTLSeconds: 900, RefreshTTLSeconds: 3600, Status: status, SecretHash: shaSecret(secret, "pepper"),
	}
}

func TestPKCEMatrix(t *testing.T) {
	if !verifyPKCE(pkceChallenge("ok"), "ok") {
		t.Fatal("S256 verifier rejected")
	}
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"plain rejected", "response_type=code&client_id=client_1&redirect_uri=https://rp.example/cb&code_challenge=x&code_challenge_method=plain"},
		{"missing challenge", "response_type=code&client_id=client_1&redirect_uri=https://rp.example/cb&code_challenge_method=S256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _, _ := fullProvider(t)
			rec := httptest.NewRecorder()
			p.HandleAuthorize(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+tc.query, nil))
			if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "error=invalid_request") {
				t.Fatalf("status=%d location=%q", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
	if verifyPKCE(pkceChallenge("ok"), "wrong") || verifyPKCE(pkceChallenge("ok"), "") {
		t.Fatal("bad PKCE verifier accepted")
	}
}

func TestAuthorizeMintRateLimitPerSession(t *testing.T) {
	p, _ := testProvider(t)
	query := "response_type=code&client_id=client_1&redirect_uri=https://rp.example/cb&code_challenge=x&code_challenge_method=S256&state=s"
	for i := 0; i < defaultMintLimit; i++ {
		rec := httptest.NewRecorder()
		p.HandleAuthorize(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+query, nil))
		if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "https://web.example/login/whatsapp#c=") {
			t.Fatalf("attempt %d status=%d location=%q", i, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := httptest.NewRecorder()
	p.HandleAuthorize(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+query, nil))
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "error=temporarily_unavailable") {
		t.Fatalf("rate limit status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAuthCodeMatrix(t *testing.T) {
	p, ps, grants, _ := fullProvider(t)
	grant := testGrant("grant_1", "client_1", "wa:dm", nil)
	grants.put(grant)
	code := "code1"
	if err := ps.StoreAuthCode(context.Background(), code, AuthCode{GrantID: grant.ID, ClientID: "client_1", RedirectURI: "https://rp.example/cb", Scopes: []string{"openid"}, CodeChallenge: pkceChallenge("verifier"), CodeChallengeMethod: "S256", ACR: "wa:dm", AuthTime: 1000}, time.Minute); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	winners := 0
	var mu sync.Mutex
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := postToken(p, url.Values{"grant_type": {"authorization_code"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "code": {code}, "redirect_uri": {"https://rp.example/cb"}, "code_verifier": {"verifier"}})
			if rec.Code == http.StatusOK {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("concurrent code winners=%d", winners)
	}
	for _, tc := range []struct {
		name string
		ac   AuthCode
		form url.Values
	}{
		{"wrong client", AuthCode{GrantID: grant.ID, ClientID: "client_1", RedirectURI: "https://rp.example/cb", CodeChallenge: pkceChallenge("v")}, url.Values{"grant_type": {"authorization_code"}, "client_id": {"client_2"}, "client_secret": {"super-secret"}, "code_verifier": {"v"}, "redirect_uri": {"https://rp.example/cb"}}},
		{"wrong redirect", AuthCode{GrantID: grant.ID, ClientID: "client_1", RedirectURI: "https://rp.example/cb", CodeChallenge: pkceChallenge("v")}, url.Values{"grant_type": {"authorization_code"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "code_verifier": {"v"}, "redirect_uri": {"https://rp.example/other"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.name
			_ = ps.StoreAuthCode(context.Background(), c, tc.ac, time.Minute)
			tc.form.Set("code", c)
			rec := postToken(p, tc.form)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_grant") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	_ = ps.StoreAuthCode(context.Background(), "expired", AuthCode{GrantID: grant.ID, ClientID: "client_1", RedirectURI: "https://rp.example/cb", CodeChallenge: pkceChallenge("v")}, time.Millisecond)
	time.Sleep(2 * time.Millisecond)
	rec := postToken(p, url.Values{"grant_type": {"authorization_code"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "code": {"expired"}, "redirect_uri": {"https://rp.example/cb"}, "code_verifier": {"v"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expired status=%d", rec.Code)
	}

	_ = ps.StoreAuthCode(context.Background(), "plain-method", AuthCode{GrantID: grant.ID, ClientID: "client_1", RedirectURI: "https://rp.example/cb", CodeChallenge: "verifier", CodeChallengeMethod: "plain"}, time.Minute)
	rec = postToken(p, url.Values{"grant_type": {"authorization_code"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "code": {"plain-method"}, "redirect_uri": {"https://rp.example/cb"}, "code_verifier": {"verifier"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_grant") {
		t.Fatalf("plain auth code status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOIDCEndToEndWithJWKSClientVerifier(t *testing.T) {
	p, ps, _, _ := fullProvider(t)
	r := chi.NewRouter()
	p.Mount(r)
	r.Get("/.well-known/oauth-jwks.json", func(w http.ResponseWriter, r *http.Request) {
		jwks, err := p.signer.JWKS(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	})
	srv := testHTTPServer(t, r)
	t.Cleanup(srv.Close)
	p.issuer = srv.URL

	verifier := "correct horse battery staple"
	authURL := srv.URL + "/oauth/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {"client_1"},
		"redirect_uri":          {"https://rp.example/cb"},
		"scope":                 {"openid profile phone offline_access"},
		"state":                 {"st_123"},
		"nonce":                 {"nonce_123"},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}.Encode()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(authURL)
	if err != nil {
		t.Skipf("httptest server unavailable in this sandbox: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status=%d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	fragment, _ := url.ParseQuery(u.Fragment)
	browserCode := fragment.Get("c")
	if browserCode == "" {
		t.Fatalf("missing browser code in %q", loc)
	}
	pendingReq, err := ps.Load(context.Background(), browserCode)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ps.ClaimVerified(context.Background(), ClaimInput{
		SessionID: "sess_1", UserCode: pendingReq.UserCode, Mode: "dm", LoginCommand: "login",
		SenderLID: "42@lid", PhoneJID: "628111@s.whatsapp.net", PhoneNumber: "+628111", PushName: "Alice",
		NowMs: p.now().UnixMilli(),
	})
	if err != nil || claim.Status != ClaimStatusVerified {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}

	resp, err = http.Post(srv.URL+"/oauth/wait/"+browserCode+"/finalize", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var finalized map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&finalized); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("finalize status=%d body=%v", resp.StatusCode, finalized)
	}
	redirect, err := url.Parse(finalized["redirect"])
	if err != nil {
		t.Fatal(err)
	}
	code := redirect.Query().Get("code")
	if code == "" || redirect.Query().Get("state") != "st_123" || redirect.Query().Get("iss") != srv.URL {
		t.Fatalf("redirect=%s", finalized["redirect"])
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {"client_1"},
		"client_secret": {"super-secret"},
		"code":          {code},
		"redirect_uri":  {"https://rp.example/cb"},
		"code_verifier": {verifier},
	}
	resp, err = http.PostForm(srv.URL+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	var tokens map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token status=%d body=%v", resp.StatusCode, tokens)
	}
	if tokens["token_type"] != "Bearer" || tokens["refresh_token"] == "" {
		t.Fatalf("token response=%v", tokens)
	}

	jwksResp, err := http.Get(srv.URL + "/.well-known/oauth-jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	set, err := jwk.ParseReader(jwksResp.Body)
	_ = jwksResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	idToken, err := jwt.Parse([]byte(tokens["id_token"].(string)),
		jwt.WithKeySet(set),
		jwt.WithValidate(true),
		jwt.WithIssuer(srv.URL),
		jwt.WithAudience("client_1"),
		jwt.WithClock(jwt.ClockFunc(p.now)),
	)
	if err != nil {
		t.Fatal(err)
	}
	sub, ok := idToken.Subject()
	if !ok || sub == "" {
		t.Fatal("id_token missing subject")
	}
	var nonce, name, phone string
	_ = idToken.Get("nonce", &nonce)
	_ = idToken.Get("name", &name)
	_ = idToken.Get("phone_number", &phone)
	if nonce != "nonce_123" || name != "Alice" || phone != "+628111" {
		t.Fatalf("id claims nonce=%q name=%q phone=%q", nonce, name, phone)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+tokens["access_token"].(string))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var userinfo map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&userinfo); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || userinfo["sub"] != sub || userinfo["name"] != "Alice" || userinfo["phone_number"] != "+628111" || userinfo["wa_jid"] != "628111@s.whatsapp.net" {
		t.Fatalf("userinfo status=%d body=%v", resp.StatusCode, userinfo)
	}

	resp, err = http.PostForm(srv.URL+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("second token status=%d", resp.StatusCode)
	}
}

func TestClientAuthMatrix(t *testing.T) {
	p, _, _, _ := fullProvider(t)
	if _, err := p.authenticateClient(basicAuthReq("client_1", "super-secret", nil)); err != nil {
		t.Fatalf("basic: %v", err)
	}
	if _, err := p.authenticateClient(formReq(url.Values{"client_id": {"client_1"}, "client_secret": {"super-secret"}})); err != nil {
		t.Fatalf("form: %v", err)
	}
	if _, err := p.authenticateClient(formReq(url.Values{"client_id": {"public_1"}})); err != nil {
		t.Fatalf("public: %v", err)
	}
	for _, req := range []*http.Request{
		basicAuthReq("client_1", "super-secret", url.Values{"client_id": {"client_1"}}),
		formReq(url.Values{"client_id": {"client_1"}, "client_secret": {"wrong"}}),
		formReq(url.Values{"client_id": {"disabled"}, "client_secret": {"super-secret"}}),
	} {
		if _, err := p.authenticateClient(req); err == nil {
			t.Fatal("bad client auth accepted")
		}
	}
}

func TestRefreshMatrix(t *testing.T) {
	p, _, grants, refresh := fullProvider(t)
	grant := testGrant("grant_1", "client_1", "wa:dm", nil)
	grants.put(grant)
	tok, err := p.issueTokens(context.Background(), oauthClient("client_1", "confidential", "super-secret", "active"), grant, []string{"openid", "profile", "offline_access"}, "", "wa:dm", 1000, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := tok["refresh_token"].(string)
	firstRaw := raw
	for i := 0; i < 3; i++ {
		rec := postToken(p, url.Values{"grant_type": {"refresh_token"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "refresh_token": {raw}})
		if rec.Code != http.StatusOK {
			t.Fatalf("hop %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		raw = body["refresh_token"].(string)
	}
	consumed := refresh.all()[0]
	rec := postToken(p, url.Values{"grant_type": {"refresh_token"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "refresh_token": {firstRaw}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reuse status=%d", rec.Code)
	}
	for _, rt := range refresh.all() {
		if rt.FamilyID == consumed.FamilyID && rt.RevokedAt == nil {
			t.Fatalf("family member not revoked: %+v", rt)
		}
	}
	p2, _, grants2, _ := fullProvider(t)
	grants2.put(grant)
	tok, _ = p2.issueTokens(context.Background(), oauthClient("client_1", "confidential", "super-secret", "active"), grant, []string{"openid", "profile", "offline_access"}, "", "wa:dm", 1000, true, nil)
	rec = postToken(p2, url.Values{"grant_type": {"refresh_token"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "refresh_token": {tok["refresh_token"].(string)}, "scope": {"openid profile"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("narrow status=%d body=%s", rec.Code, rec.Body.String())
	}
	tok, _ = p2.issueTokens(context.Background(), oauthClient("client_1", "confidential", "super-secret", "active"), grant, []string{"openid", "offline_access"}, "", "wa:dm", 1000, true, nil)
	rec = postToken(p2, url.Values{"grant_type": {"refresh_token"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "refresh_token": {tok["refresh_token"].(string)}, "scope": {"openid phone"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_scope") {
		t.Fatalf("widen status=%d body=%s", rec.Code, rec.Body.String())
	}
	grants2.revoke(grant.ID)
	tok, _ = p2.issueTokens(context.Background(), oauthClient("client_1", "confidential", "super-secret", "active"), grant, []string{"openid", "offline_access"}, "", "wa:dm", 1000, true, nil)
	rec = postToken(p2, url.Values{"grant_type": {"refresh_token"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "refresh_token": {tok["refresh_token"].(string)}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("revoked grant status=%d", rec.Code)
	}

	p3, _, grants3, refresh3 := fullProvider(t)
	grants3.put(grant)
	tok, _ = p3.issueTokens(context.Background(), oauthClient("client_1", "confidential", "super-secret", "active"), grant, []string{"openid", "offline_access"}, "", "wa:dm", 1000, true, nil)
	raw = tok["refresh_token"].(string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := postToken(p3, url.Values{"grant_type": {"refresh_token"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "refresh_token": {raw}})
			if rec.Code == http.StatusOK {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("concurrent refresh winners=%d", winners)
	}
	family := refresh3.all()[0].FamilyID
	members, consumedCount, activeCount := 0, 0, 0
	for _, rt := range refresh3.all() {
		if rt.FamilyID != family {
			continue
		}
		members++
		if rt.ConsumedAt != nil {
			consumedCount++
		}
		if rt.RevokedAt == nil {
			activeCount++
		}
	}
	if members != 2 || consumedCount != 1 || activeCount != 0 {
		t.Fatalf("concurrent refresh family members=%d consumed=%d active=%d all=%+v", members, consumedCount, activeCount, refresh3.all())
	}

	p4, _, grants4, refresh4 := fullProvider(t)
	grants4.put(grant)
	expiredRaw := "expired.refresh"
	_ = refresh4.Create(context.Background(), domain.OAuthRefreshToken{ID: "rt_expired", GrantID: grant.ID, OrganizationID: "org_1", TokenHash: shaBytes(expiredRaw), FamilyID: "fam_expired", Scopes: json.RawMessage(`["openid"]`), IssuedAt: 1, ExpiresAt: 2})
	rec = postToken(p4, url.Values{"grant_type": {"refresh_token"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "refresh_token": {expiredRaw}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expired refresh status=%d", rec.Code)
	}
}

func TestRevokedGrantBusCacheBlocksRefreshImmediately(t *testing.T) {
	p, _, grants, _ := fullProvider(t)
	grant := testGrant("grant_1", "client_1", "wa:dm", nil)
	grants.put(grant)
	tok, err := p.issueTokens(context.Background(), oauthClient("client_1", "confidential", "super-secret", "active"), grant, []string{"openid", "offline_access"}, "", "wa:dm", 1000, true, nil)
	if err != nil {
		t.Fatal(err)
	}

	p.MarkGrantRevoked(grant.ID)
	rec := postToken(p, url.Values{"grant_type": {"refresh_token"}, "client_id": {"client_1"}, "client_secret": {"super-secret"}, "refresh_token": {tok["refresh_token"].(string)}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_grant") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestClaimsByScopeAndACRForIDTokenAndUserInfo(t *testing.T) {
	for _, acr := range []string{"wa:dm", "wa:group"} {
		for _, scopes := range [][]string{{"openid"}, {"openid", "profile"}, {"openid", "phone"}, {"openid", "wa:group"}, {"openid", "profile", "phone", "wa:group"}} {
			t.Run(acr+"/"+strings.Join(scopes, "_"), func(t *testing.T) {
				p, _, grants, _ := fullProvider(t)
				var group *string
				if acr == "wa:group" {
					group = strp("120@g.us")
				}
				grant := testGrant("grant_1", "client_1", acr, group)
				grants.put(grant)
				tok, err := p.issueTokens(context.Background(), oauthClient("client_1", "confidential", "super-secret", "active"), grant, scopes, "nonce", acr, 1000, false, nil)
				if err != nil {
					t.Fatal(err)
				}
				idClaims := jwtPayload(t, tok["id_token"].(string))
				assertClaimSet(t, idClaims, scopes, acr)
				req := httptest.NewRequest(http.MethodGet, "/oauth/userinfo", nil)
				req.Header.Set("Authorization", "Bearer "+tok["access_token"].(string))
				rec := httptest.NewRecorder()
				p.HandleUserInfo(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("userinfo status=%d body=%s", rec.Code, rec.Body.String())
				}
				var ui map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &ui)
				assertClaimSet(t, ui, scopes, acr)
				if _, ok := idClaims["wa_identity_id"]; ok {
					t.Fatal("id_token leaked wa_identity_id")
				}
				if _, ok := ui["wa_identity_id"]; ok {
					t.Fatal("userinfo leaked wa_identity_id")
				}
			})
		}
	}
}

func TestPairwiseSubStableAndClientScoped(t *testing.T) {
	p, _, _, _ := fullProvider(t)
	a := p.pairwiseSub("client_1", 42)
	if a != p.pairwiseSub("client_1", 42) {
		t.Fatal("pairwise sub unstable")
	}
	if a == p.pairwiseSub("client_2", 42) {
		t.Fatal("pairwise sub reused across clients")
	}
}

func TestFinalizeMatrix(t *testing.T) {
	p, ps, _, _ := fullProvider(t)
	req := PendingRequest{ClientID: "client_1", OrganizationID: "org_1", BrowserCode: "browser", SessionID: "sess_1", UserCode: "483920", LoginCommand: "login", Mode: "dm", RedirectURI: "https://rp.example/cb", CodeChallenge: pkceChallenge("v"), Scopes: []string{"openid"}, Status: PendingStatusPending, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
	if err := ps.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	res, err := ps.ClaimVerified(context.Background(), ClaimInput{SessionID: "sess_1", UserCode: "483920", Mode: "dm", LoginCommand: "login", SenderLID: "42@lid", NowMs: 1000000})
	if err != nil || res.Status != ClaimStatusVerified {
		t.Fatalf("claim=%+v err=%v", res, err)
	}
	rec1 := httptest.NewRecorder()
	p.HandleFinalize(rec1, finalizeReq("browser"))
	rec2 := httptest.NewRecorder()
	p.HandleFinalize(rec2, finalizeReq("browser"))
	if rec1.Code != http.StatusOK || rec2.Code != http.StatusOK {
		t.Fatalf("double finalize statuses=%d,%d", rec1.Code, rec2.Code)
	}
	var first, second map[string]string
	_ = json.Unmarshal(rec1.Body.Bytes(), &first)
	_ = json.Unmarshal(rec2.Body.Bytes(), &second)
	if first["redirect"] == "" || first["redirect"] != second["redirect"] {
		t.Fatalf("redirects differ: %q %q", first["redirect"], second["redirect"])
	}
	u, _ := url.Parse(first["redirect"])
	code := u.Query().Get("code")
	if _, err := ps.RedeemAuthCode(context.Background(), code); err != nil {
		t.Fatalf("first auth code redeem: %v", err)
	}
	if _, err := ps.RedeemAuthCode(context.Background(), code); err == nil {
		t.Fatal("auth code redeemed twice")
	}
	rec := httptest.NewRecorder()
	p.HandleFinalize(rec, finalizeReq("missing"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expired/missing finalize status=%d", rec.Code)
	}
}

func TestResolveModeTokenMatchesACRValues(t *testing.T) {
	if got, err := resolveMode("dm,group", "not-wa:group wa:dm"); err != nil || got != "dm" {
		t.Fatalf("substring acr selected group or failed: got=%q err=%v", got, err)
	}
	if got, err := resolveMode("dm,group", "wa:dm wa:group"); err != nil || got != "group" {
		t.Fatalf("group precedence failed: got=%q err=%v", got, err)
	}
}

func TestUserInfoRejectsIDToken(t *testing.T) {
	p, _, grants, _ := fullProvider(t)
	grant := testGrant("grant_1", "client_1", "wa:dm", nil)
	grants.put(grant)
	tok, err := p.issueTokens(context.Background(), oauthClient("client_1", "confidential", "super-secret", "active"), grant, []string{"openid"}, "", "wa:dm", 1000, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+tok["id_token"].(string))
	rec := httptest.NewRecorder()
	p.HandleUserInfo(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("userinfo accepted id_token: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProviderClientIPTrustProxyModes(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/oauth/wait/x/stream", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.2")
	if got := NewProvider(ProviderConfig{}).clientIP(req); got != "10.0.0.2" {
		t.Fatalf("default trusted XFF: %q", got)
	}
	if got := NewProvider(ProviderConfig{TrustProxy: true}).clientIP(req); got != "203.0.113.9" {
		t.Fatalf("trusted proxy ignored XFF: %q", got)
	}
}

func TestConcurrentUserCodeMintReservesUniqueCodes(t *testing.T) {
	p, _, _, _ := fullProvider(t)
	const n = 48
	exp := time.Now().Add(time.Minute).UnixMilli()
	var wg sync.WaitGroup
	codes := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, err := p.NewUserCode(context.Background(), "sess_1", exp)
			if err != nil {
				t.Errorf("NewUserCode: %v", err)
				return
			}
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	seen := map[string]bool{}
	for code := range codes {
		if seen[code] {
			t.Fatalf("duplicate user code minted: %s", code)
		}
		seen[code] = true
	}
	if len(seen) != n {
		t.Fatalf("minted %d codes, want %d", len(seen), n)
	}
}

func TestUserInfoBearerRejectionMatrix(t *testing.T) {
	p, _, grants, _ := fullProvider(t)
	grant := testGrant("grant_1", "client_1", "wa:dm", nil)
	grants.put(grant)
	// Craft exp relative to the provider's injected clock (real-now), so `expired`
	// is genuinely past and `badIss` is unexpired-but-wrong-issuer.
	badIss, _ := p.signer.SignJWT(context.Background(), map[string]any{"iss": "https://other.example", "aud": "client_1", "sub": grant.Sub, "exp": p.now().Add(time.Hour).Unix(), "typ": "access", "scope": "openid", "grant_id": grant.ID})
	expired, _ := p.signer.SignJWT(context.Background(), map[string]any{"iss": p.issuer, "aud": "client_1", "sub": grant.Sub, "exp": p.now().Add(-time.Hour).Unix(), "typ": "access", "scope": "openid", "grant_id": grant.ID})
	for _, bearer := range []string{"", "bad.token.value", badIss, expired} {
		req := httptest.NewRequest(http.MethodGet, "/oauth/userinfo", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		p.HandleUserInfo(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("bearer %q status=%d", bearer, rec.Code)
		}
	}
}

func TestRevokeMatrix(t *testing.T) {
	p, _, grants, refresh := fullProvider(t)
	grant := testGrant("grant_1", "client_1", "wa:dm", nil)
	grants.put(grant)
	tok, _ := p.issueTokens(context.Background(), oauthClient("client_1", "confidential", "super-secret", "active"), grant, []string{"openid", "offline_access"}, "", "wa:dm", 1000, true, nil)
	for _, token := range []string{tok["refresh_token"].(string), tok["access_token"].(string), "unknown"} {
		rec := postRevoke(p, url.Values{"client_id": {"client_1"}, "client_secret": {"super-secret"}, "token": {token}})
		if rec.Code != http.StatusOK {
			t.Fatalf("revoke %q status=%d", token, rec.Code)
		}
	}
	for _, rt := range refresh.all() {
		if rt.RevokedAt == nil {
			t.Fatalf("refresh family not revoked: %+v", rt)
		}
	}
}

func testGrant(id, clientID, acr string, group *string) domain.OAuthGrant {
	return domain.OAuthGrant{ID: id, OrganizationID: "org_1", ClientID: clientID, WAIdentityID: 42, Sub: "sub_" + clientID, GrantedScopes: json.RawMessage(`["openid"]`), LastACR: acr, LastGroupJID: group}
}

func pkceChallenge(verifier string) string {
	sum := shaBytes(verifier)
	return base64.RawURLEncoding.EncodeToString(sum)
}

func postToken(p *Provider, form url.Values) *httptest.ResponseRecorder {
	req := formReq(form)
	rec := httptest.NewRecorder()
	p.HandleToken(rec, req)
	return rec
}

func postRevoke(p *Provider, form url.Values) *httptest.ResponseRecorder {
	req := formReq(form)
	rec := httptest.NewRecorder()
	p.HandleRevoke(rec, req)
	return rec
}

func formReq(form url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = req.ParseForm()
	return req
}

func basicAuthReq(id, secret string, form url.Values) *http.Request {
	req := formReq(form)
	req.SetBasicAuth(id, secret)
	return req
}

func jwtPayload(t *testing.T, raw string) map[string]any {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts=%d", len(parts))
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertClaimSet(t *testing.T, claims map[string]any, scopes []string, acr string) {
	t.Helper()
	if _, ok := claims["sub"]; !ok {
		t.Fatal("missing sub")
	}
	if hasScope(scopes, "profile") && claims["name"] != "Alice" {
		t.Fatalf("name=%v", claims["name"])
	}
	if !hasScope(scopes, "profile") && claims["name"] != nil {
		t.Fatalf("unexpected name=%v", claims["name"])
	}
	if hasScope(scopes, "phone") {
		if claims["phone_number"] != "+628111" || claims["phone_number_verified"] != true || claims["wa_jid"] != "628111@s.whatsapp.net" {
			t.Fatalf("phone claims=%#v", claims)
		}
	} else if claims["phone_number"] != nil || claims["wa_jid"] != nil {
		t.Fatalf("unexpected phone claims=%#v", claims)
	}
	if hasScope(scopes, "wa:group") && acr == "wa:group" {
		if claims["wa_group_verified"] != true || claims["wa_group_id"] != "120@g.us" || claims["wa_group_name"] != "Members" {
			t.Fatalf("group claims=%#v", claims)
		}
	} else if claims["wa_group_verified"] != nil || claims["wa_group_id"] != nil || claims["wa_group_name"] != nil {
		t.Fatalf("unexpected group claims=%#v", claims)
	}
}

func strp(s string) *string { return &s }

func finalizeReq(browserCode string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/oauth/wait/"+browserCode+"/finalize", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("browser_code", browserCode)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}
