package router

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/coder/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/assertion"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/stream"
)

func newRealtimeServer(t *testing.T, principal *authz.Principal, sessions fakeSessions) (*Server, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	_, priv, _ := ed25519.GenerateKey(nil)
	m, _ := assertion.NewMinter(priv, "router")
	srv, err := NewServer(Config{
		Sessions: sessions,
		Gateways: fakeGateways{},
		Minter:   m,
		Tokens:   fakeTokens{p: principal},
		Redis:    rdb,
		Pump:     stream.NewPump(stream.PumpConfig{Redis: rdb}),
		Registry: stream.NewConnRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, rdb
}

func ownerPrincipal() *authz.Principal {
	return &authz.Principal{Kind: authz.KindUser, OrganizationID: "org_1", UserID: "u1", OrgRole: authz.OrgRoleOwner}
}

func mint(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/realtime/ticket", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer aaa.bbb.ccc")
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

// TestTicketMint_OrganizationScope verifies the ticket mint organization scope behavior remains part of the package contract.
// It composes the router with deterministic resolvers, credentials, and upstreams, then observes the edge response.
// This protects the single trust and routing boundary from cross-tenant leaks, hangs, or proxy contract drift.
func TestTicketMint_OrganizationScope(t *testing.T) {
	srv, rdb := newRealtimeServer(t, ownerPrincipal(), fakeSessions{m: map[string]domain.WASession{}})
	rec := mint(t, srv, `{"scope":"organization"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var resp ticketResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	raw, err := rdb.Get(context.Background(), srv.ticketKey(resp.Ticket)).Result()
	if err != nil {
		t.Fatalf("ticket not stored: %v", err)
	}
	var tk ticket
	_ = json.Unmarshal([]byte(raw), &tk)
	if tk.Scope != "organization" || tk.Organization != "org_1" {
		t.Fatalf("resolved ticket = %+v", tk)
	}
}

// TestTicketMint_FirehoseRequiresSuperAdmin verifies the ticket mint firehose requires super admin behavior remains part of the package contract.
// It composes the router with deterministic resolvers, credentials, and upstreams, then observes the edge response.
// This protects the single trust and routing boundary from cross-tenant leaks, hangs, or proxy contract drift.
func TestTicketMint_FirehoseRequiresSuperAdmin(t *testing.T) {
	srv, _ := newRealtimeServer(t, ownerPrincipal(), fakeSessions{m: map[string]domain.WASession{}})
	rec := mint(t, srv, `{"scope":"firehose"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin firehose status=%d, want 403", rec.Code)
	}

	admin := &authz.Principal{Kind: authz.KindUser, OrganizationID: "org_1", UserID: "su", PlatformRole: authz.PlatformRoleSuperAdmin}
	srv2, _ := newRealtimeServer(t, admin, fakeSessions{m: map[string]domain.WASession{}})
	rec2 := mint(t, srv2, `{"scope":"firehose"}`)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("admin firehose status=%d, want 201", rec2.Code)
	}
}

// TestTicketMint_SessionScope_OrgIsolation verifies tenant or target isolation cannot be bypassed across trust scopes.
// It composes the router with deterministic resolvers, credentials, and upstreams, then observes the edge response.
// This protects the single trust and routing boundary from cross-tenant leaks, hangs, or proxy contract drift.
func TestTicketMint_SessionScope_OrgIsolation(t *testing.T) {
	sessions := fakeSessions{m: map[string]domain.WASession{
		"wa_other": {ID: "wa_other", OrganizationID: "org_2"},
	}}
	srv, _ := newRealtimeServer(t, ownerPrincipal(), sessions)
	rec := mint(t, srv, `{"scope":"session","session":"wa_other"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org session ticket status=%d, want 404", rec.Code)
	}
}

// TestRealtime_EndToEnd_WS verifies the valid realtime flow and its observable contract.
// It composes the router with deterministic resolvers, credentials, and upstreams, then observes the edge response.
// This protects the single trust and routing boundary from cross-tenant leaks, hangs, or proxy contract drift.
func TestRealtime_EndToEnd_WS(t *testing.T) {
	srv, rdb := newRealtimeServer(t, ownerPrincipal(), fakeSessions{m: map[string]domain.WASession{}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Mint via HTTP.
	body := strings.NewReader(`{"scope":"organization"}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/v1/realtime/ticket", body)
	req.Header.Set("Authorization", "Bearer aaa.bbb.ccc")
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tr ticketResponse
	_ = json.NewDecoder(httpResp.Body).Decode(&tr)
	httpResp.Body.Close()
	if tr.Ticket == "" {
		t.Fatal("no ticket minted")
	}

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/realtime?ticket=" + tr.Ticket
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()

	// First frame is the "connected" envelope.
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read connected: %v", err)
	}
	var first map[string]any
	_ = json.Unmarshal(data, &first)
	if first["event"] != "connected" {
		t.Fatalf("first frame = %v, want connected", first["event"])
	}

	// Publish an event on the org's channel; the subscriber should deliver it.
	ev := domain.NewEvent(domain.EventMessage, "wa_1", "org_1", map[string]any{"hi": true})
	payload, _ := json.Marshal(ev)
	if err := rdb.Publish(ctx, "evt:org_1:wa_1", payload).Err(); err != nil {
		t.Fatal(err)
	}

	_, data2, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	var got domain.Event
	_ = json.Unmarshal(data2, &got)
	if got.ID != ev.ID || got.Type != domain.EventMessage {
		t.Fatalf("delivered event = %+v, want id=%s", got, ev.ID)
	}

	// Single-use: a second dial with the same ticket must be rejected.
	if c2, _, err := websocket.Dial(ctx, wsURL, nil); err == nil {
		c2.CloseNow()
		t.Fatal("expected single-use ticket to reject the second connection")
	}
}
