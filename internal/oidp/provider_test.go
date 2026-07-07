package oidp

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
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

func testProvider(t *testing.T) (*Provider, *PendingStore) {
	t.Helper()
	_, rdb := testRedis(t)
	now := time.UnixMilli(100000)
	client := domain.OAuthClient{
		ClientID: "client_1", OrganizationID: "org_1", SessionID: "sess_1", Name: "Acme",
		LoginCommand: "login", RedirectURIs: json.RawMessage(`["https://rp.example/cb"]`),
		Modes: "dm,group", GroupJID: strp("120@g.us"), AllowedScopes: json.RawMessage(`["openid","profile","phone","wa:group"]`),
		Status: "active",
	}
	pending := NewPendingStore(rdb, "test", 10*time.Minute)
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

func strp(s string) *string { return &s }
