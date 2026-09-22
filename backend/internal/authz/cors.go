package authz

import (
	"net/http"
	"strconv"
	"strings"
)

// corsMaxAge is how long (seconds) a browser may cache a preflight result.
const corsMaxAge = 600

// Keep this list explicit: reflecting Access-Control-Request-Headers would let
// browser callers opt arbitrary internal headers into the CORS policy.
const corsAllowedHeaders = "Authorization, Content-Type, X-Api-Key, Idempotency-Key, X-Request-Id"

// CORS returns chi-compatible middleware that allows the configured frontend
// origins to call the gateway directly from the browser (§4.4) — the dashboard
// opens the NDJSON stream and issues actions cross-origin with a Bearer JWT.
//
// It reflects an allowed Origin (so credentials may be used), permits the
// Authorization header, and answers preflight (OPTIONS) requests. Server-to-
// server traffic (webhooks out, programmatic in) carries no Origin and is
// unaffected. An empty origins list disables CORS (same-origin / proxied only).
func CORS(origins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(origins))
	wildcard := false
	for _, o := range origins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if o == "*" {
			wildcard = true
			continue
		}
		allowed[strings.TrimRight(o, "/")] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && originAllowed(origin, allowed, wildcard) {
				h := w.Header()
				// Reflect the origin (not "*") so credentialed requests are valid.
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Add("Vary", "Origin")
				if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
					h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
					h.Set("Access-Control-Allow-Headers", corsAllowedHeaders)
					h.Set("Access-Control-Max-Age", strconv.Itoa(corsMaxAge))
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func originAllowed(origin string, allowed map[string]struct{}, wildcard bool) bool {
	if wildcard {
		return true
	}
	_, ok := allowed[strings.TrimRight(origin, "/")]
	return ok
}
