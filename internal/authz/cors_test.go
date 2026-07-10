package authz

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func corsNext() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// TestCORS table-tests origin matching, fixed preflight headers, and server-to-server pass-through.
// It supplies controlled credentials or repository results and observes the resolved principal or denial.
// This protects the caller-authentication boundary from fail-open behavior and upstream contract drift.
func TestCORS(t *testing.T) {
	origins := []string{"https://app.example", "https://admin.example/"}

	tests := []struct {
		name            string
		method          string
		origin          string
		preflight       bool // sets Access-Control-Request-Method
		wantAllowOrigin string
		wantStatus      int
	}{
		{
			name: "allowed origin reflected", method: http.MethodGet, origin: "https://app.example",
			wantAllowOrigin: "https://app.example", wantStatus: http.StatusOK,
		},
		{
			name: "allowed origin with trailing slash in config", method: http.MethodGet, origin: "https://admin.example",
			wantAllowOrigin: "https://admin.example", wantStatus: http.StatusOK,
		},
		{
			name: "disallowed origin not reflected", method: http.MethodGet, origin: "https://evil.example",
			wantAllowOrigin: "", wantStatus: http.StatusOK,
		},
		{
			name: "no origin header (server-to-server)", method: http.MethodGet, origin: "",
			wantAllowOrigin: "", wantStatus: http.StatusOK,
		},
		{
			name: "preflight allowed origin short-circuits 204", method: http.MethodOptions, origin: "https://app.example",
			preflight: true, wantAllowOrigin: "https://app.example", wantStatus: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := CORS(origins)(corsNext())
			r := httptest.NewRequest(tt.method, "/api/v1/sessions", nil)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.preflight {
				r.Header.Set("Access-Control-Request-Method", "POST")
				r.Header.Set("Access-Control-Request-Headers", "Authorization")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.wantAllowOrigin {
				t.Errorf("Allow-Origin = %q, want %q", got, tt.wantAllowOrigin)
			}
			if tt.wantAllowOrigin != "" {
				if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
					t.Errorf("expected Allow-Credentials true for allowed origin")
				}
			}
			if tt.preflight && tt.wantStatus == http.StatusNoContent {
				if got := rec.Header().Get("Access-Control-Allow-Headers"); got != corsAllowedHeaders {
					t.Errorf("Allow-Headers = %q, want %q", got, corsAllowedHeaders)
				}
			}
		})
	}
}

// TestCORS_Wildcard verifies wildcard configuration still reflects the credentialed request origin.
// It supplies controlled credentials or repository results and observes the resolved principal or denial.
// This protects the caller-authentication boundary from fail-open behavior and upstream contract drift.
func TestCORS_Wildcard(t *testing.T) {
	h := CORS([]string{"*"})(corsNext())
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://anything.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://anything.example" {
		t.Errorf("wildcard Allow-Origin = %q, want reflected origin", got)
	}
}

// TestCORS_Disabled verifies an empty allow-list emits no cross-origin authorization.
// It supplies controlled credentials or repository results and observes the resolved principal or denial.
// This protects the caller-authentication boundary from fail-open behavior and upstream contract drift.
func TestCORS_Disabled(t *testing.T) {
	h := CORS(nil)(corsNext())
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://app.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("with no configured origins, Allow-Origin = %q, want empty", got)
	}
}
