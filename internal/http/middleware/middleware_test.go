package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func decodeErr(t *testing.T, body io.Reader) domain.ErrorBody {
	t.Helper()
	var b domain.ErrorBody
	if err := json.NewDecoder(body).Decode(&b); err != nil {
		t.Fatalf("decode err body: %v", err)
	}
	return b
}

// --- fakes -------------------------------------------------------------------

type fakeVerifier struct {
	key   *domain.APIKey
	orgID string
	err   error
}

func (f fakeVerifier) Verify(_ context.Context, _ string) (*domain.APIKey, string, error) {
	return f.key, f.orgID, f.err
}

type fakeLimiter struct {
	allow  bool
	err    error
	gotKey string
}

func (f *fakeLimiter) Allow(_ context.Context, key string) (bool, error) {
	f.gotKey = key
	return f.allow, f.err
}

// --- Recover -----------------------------------------------------------------

func TestRecover(t *testing.T) {
	panicker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := Recover(discardLogger())(panicker)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if b := decodeErr(t, rec.Body); b.Error == nil || b.Error.Code != domain.CodeInternal {
		t.Fatalf("body = %+v", b.Error)
	}
}

// --- RequestID ---------------------------------------------------------------

func TestRequestIDGenerates(t *testing.T) {
	var seen string
	h := RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestID(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if seen == "" {
		t.Fatal("expected request id in context")
	}
	if rec.Header().Get(RequestIDHeader) != seen {
		t.Fatalf("response header %q != ctx %q", rec.Header().Get(RequestIDHeader), seen)
	}
}

func TestRequestIDPropagatesInbound(t *testing.T) {
	var seen string
	h := RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestID(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(RequestIDHeader, "client-supplied-id")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seen != "client-supplied-id" {
		t.Fatalf("ctx id = %q, want client-supplied-id", seen)
	}
	if rec.Header().Get(RequestIDHeader) != "client-supplied-id" {
		t.Fatalf("echoed header = %q", rec.Header().Get(RequestIDHeader))
	}
}

// --- Logger ------------------------------------------------------------------

func TestLoggerPassThrough(t *testing.T) {
	h := Logger(discardLogger())(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("logger altered response: %d %q", rec.Code, rec.Body.String())
	}
}

// --- APIKeyAuth --------------------------------------------------------------

func TestAPIKeyAuthValid(t *testing.T) {
	v := fakeVerifier{
		key:   &domain.APIKey{ID: "key_1", Permissions: domain.Permissions{Read: true}},
		orgID: "org_1",
	}
	var gotOrganization string
	var gotKey *domain.APIKey
	h := APIKeyAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrganization = httpx.OrganizationID(r.Context())
		gotKey = httpx.APIKeyCtx(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer wak_abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotOrganization != "org_1" || gotKey == nil || gotKey.ID != "key_1" {
		t.Fatalf("ctx not populated: organization=%q key=%+v", gotOrganization, gotKey)
	}
}

func TestAPIKeyAuthMissingHeader(t *testing.T) {
	h := APIKeyAuth(fakeVerifier{})(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAPIKeyAuthMalformedHeader(t *testing.T) {
	for _, hdr := range []string{"Token xyz", "Bearer", "Bearer ", "basic abc"} {
		h := APIKeyAuth(fakeVerifier{})(okHandler())
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", hdr)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("header %q: status = %d, want 401", hdr, rec.Code)
		}
	}
}

func TestAPIKeyAuthCaseInsensitiveScheme(t *testing.T) {
	v := fakeVerifier{key: &domain.APIKey{ID: "k"}, orgID: "org_1"}
	h := APIKeyAuth(v)(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "bEaReR wak_abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestAPIKeyAuthVerifierRejects(t *testing.T) {
	h := APIKeyAuth(fakeVerifier{err: errors.New("nope")})(okHandler())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer wak_bad")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// --- permission gates --------------------------------------------------------

func withAPIKey(perms domain.Permissions) *http.Request {
	req := httptest.NewRequest("GET", "/", nil)
	ctx := httpx.SetAPIKey(req.Context(), &domain.APIKey{ID: "k", Permissions: perms})
	return req.WithContext(ctx)
}

func TestPermissionGatesAllow(t *testing.T) {
	cases := []struct {
		name string
		mw   func() func(http.Handler) http.Handler
		perm domain.Permissions
	}{
		{"read", RequireRead, domain.Permissions{Read: true}},
		{"send", RequireSend, domain.Permissions{Send: true}},
		{"manage", RequireManage, domain.Permissions{Manage: true}},
		{"events", RequireEvents, domain.Permissions{Events: true}},
	}
	for _, c := range cases {
		t.Run(c.name+"/allow", func(t *testing.T) {
			h := c.mw()(okHandler())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, withAPIKey(c.perm))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
		})
		t.Run(c.name+"/deny", func(t *testing.T) {
			h := c.mw()(okHandler())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, withAPIKey(domain.Permissions{})) // no perms
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
		})
	}
}

func TestPermissionGateNoKey401(t *testing.T) {
	h := RequireRead()(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// --- RateLimit ---------------------------------------------------------------

func TestRateLimitAllow(t *testing.T) {
	lim := &fakeLimiter{allow: true}
	h := RateLimit(lim, nil)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRateLimitDeny(t *testing.T) {
	lim := &fakeLimiter{allow: false}
	h := RateLimit(lim, nil)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
}

func TestRateLimitFailsOpenOnError(t *testing.T) {
	lim := &fakeLimiter{allow: false, err: errors.New("redis down")}
	h := RateLimit(lim, nil)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open)", rec.Code)
	}
}

func TestRateLimitKeyBySession(t *testing.T) {
	lim := &fakeLimiter{allow: true}
	r := chi.NewRouter()
	r.With(RateLimit(lim, nil)).Get("/sessions/{session}/send", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/sessions/ses_42/send", nil))
	if lim.gotKey != "session:ses_42" {
		t.Fatalf("key = %q, want session:ses_42", lim.gotKey)
	}
}

func TestRateLimitKeyByOrganization(t *testing.T) {
	lim := &fakeLimiter{allow: true}
	h := RateLimit(lim, nil)(okHandler())
	req := httptest.NewRequest("GET", "/webhooks", nil)
	req = req.WithContext(httpx.SetOrganizationID(req.Context(), "tnt_9"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if lim.gotKey != "organization:tnt_9" {
		t.Fatalf("key = %q, want organization:tnt_9", lim.gotKey)
	}
}

// --- CookieSession -----------------------------------------------------------

func TestCookieSessionSetsOrganization(t *testing.T) {
	// optionalAuth is a no-op passthrough here; organizationFrom reports a organization.
	optionalAuth := func(next http.Handler) http.Handler { return next }
	organizationFrom := func(*http.Request) (string, bool) { return "tnt_cookie", true }

	var seen string
	h := CookieSession(optionalAuth, organizationFrom)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpx.OrganizationID(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if seen != "tnt_cookie" {
		t.Fatalf("organization = %q, want tnt_cookie", seen)
	}
}

func TestCookieSessionNoOrganizationNeverRejects(t *testing.T) {
	optionalAuth := func(next http.Handler) http.Handler { return next }
	organizationFrom := func(*http.Request) (string, bool) { return "", false }
	h := CookieSession(optionalAuth, organizationFrom)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (never rejects)", rec.Code)
	}
}

func TestRequireOrganization(t *testing.T) {
	h := RequireOrganization()(okHandler())
	// no organization -> 401
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	// organization present -> pass
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(httpx.SetOrganizationID(req.Context(), "tnt_1"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
