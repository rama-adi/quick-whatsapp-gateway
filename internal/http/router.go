package httpx

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/middleware"
	shttpx "github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

// RouterConfig groups the gateway HTTP engine's handlers and edge middleware.
// In production Auth must be assertion.Middleware; nil exists only for isolated
// tests and intentionally leaves API routes ungated. Readiness, OpenAPI serving,
// limiting, and logging are optional, while RequestTimeout always resolves to a
// bounded positive deadline.
type RouterConfig struct {
	Handlers *handlers.Handlers

	// Auth authenticates every /api/v1 request and populates the request-context
	// Principal the capability gates read. After the central-router cutover this is
	// the internal-assertion middleware (assertion.Middleware): the gateway no
	// longer verifies end-user JWTs/api-keys — the router does, then vouches a
	// resolved Principal via a signed assertion (docs/specs/router.md, D2/D4). A
	// nil Auth leaves the API surface ungated (tests only).
	Auth func(http.Handler) http.Handler

	// Limiter is the per-session/organization send limiter for API routes (optional;
	// nil disables HTTP-edge rate limiting — the outbound pipeline still limits).
	Limiter middleware.RateLimiter

	// Readiness pings backends for /readyz. nil => always ready.
	Readiness func() error

	// OpenAPIPath is the on-disk path to docs/openapi.yaml (served at
	// /api/v1/openapi.yaml). Empty disables the route.
	OpenAPIPath string

	// RequestTimeout bounds the lifetime of every authenticated API request; the
	// deadline propagates into ctx-aware store queries so a wedged downstream call
	// returns 503 instead of hanging. Zero uses middleware.DefaultRequestTimeout.
	RequestTimeout time.Duration

	Log *slog.Logger
}

// NewRouter builds the gateway's full chi surface per §11. The gateway is a pure
// WhatsApp engine: it has no human login, auth callback, or SPA. Production
// /api/v1 requests arrive from the central router and are authenticated by a
// request-bound internal assertion before huma capability gates run; health,
// readiness, metrics, and the optional OpenAPI file remain outside that group.
// Middleware order is load-bearing: recovery wraps instrumentation, timeout and
// assertion context wrap operations, and rate limiting runs only when configured.
func NewRouter(cfg RouterConfig) http.Handler {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	h := cfg.Handlers

	r := chi.NewRouter()
	// Base stack: Recover MUST be outermost so a panic anywhere below is caught.
	// No CORS here: browsers reach the router, not the gateway — the gateway only
	// serves the internal API (called by the router under a signed assertion) plus
	// the unauthenticated health/metrics probes.
	r.Use(middleware.Recover(log))
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger(log))

	// Health / readiness / metrics (unauthenticated).
	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz(cfg.Readiness))
	r.Handle("/metrics", promhttp.Handler())

	// The OpenAPI contract (served by the gateway only when OpenAPIPath is set;
	// normally the router serves it). Kept on the /api/v1 chi route.
	if cfg.OpenAPIPath != "" {
		r.Route("/api/v1", func(api chi.Router) {
			api.Get("/openapi.yaml", serveFile(cfg.OpenAPIPath, "application/yaml"))
		})
	}

	// The whole JSON API is now code-first huma operations (D11): each declares its
	// own full "/api/v1/…" path and capability gate, so they mount on the ROOT
	// router (mounting inside chi.Route("/api/v1") would double-prefix). cfg.Auth
	// (the assertion-verify middleware) + the optional edge limiter run first.
	timeout := cfg.RequestTimeout
	if timeout == 0 {
		timeout = middleware.DefaultRequestTimeout
	}
	r.Group(func(authed chi.Router) {
		// Bounded request lifetime FIRST (inside the base recover/logger stack, above
		// auth): every API request gets a context deadline that flows into the
		// ctx-aware store queries, so a wedged MySQL call is cancelled and surfaces as
		// a 503 instead of hanging the caller indefinitely. Health/metrics/openapi
		// routes above are intentionally excluded — they never touch the wedged path.
		authed.Use(middleware.Timeout(timeout))
		if cfg.Auth != nil {
			authed.Use(cfg.Auth)
		}
		if cfg.Limiter != nil {
			authed.Use(middleware.RateLimit(cfg.Limiter, nil))
		}
		hapi := humax.NewAPI(authed)
		handlers.RegisterAllOps(hapi, h)
	})

	// No SPA, no /auth: any other path is a JSON 404.
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		shttpx.WriteError(w, domain.ErrNotFound("route not found"))
	})

	return r
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func readyz(ping func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if ping != nil {
			if err := ping(); err != nil {
				shttpx.WriteError(w, domain.ErrInternal("not ready: "+err.Error()))
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	}
}

func serveFile(path, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(path)
		if err != nil {
			shttpx.WriteError(w, domain.ErrNotFound("file not found"))
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(b)
	}
}
