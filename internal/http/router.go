package httpx

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/middleware"
	shttpx "github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

// RouterConfig groups everything the router needs from the composition root.
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

	Log *slog.Logger
}

// NewRouter builds the full chi router per §11. The gateway is a pure WhatsApp
// engine with NO human login: it has no /auth surface and serves no SPA. The
// JSON API under /api/v1 is gated by the two-acceptor auth middleware
// (JWKS-verified JWT OR better-auth api-key) plus the authz capability gates;
// health/metrics probes stay unauthenticated.
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

	// JSON API under /api/v1. Every route is authenticated by cfg.Auth (the router
	// assertion-verify middleware), then authorized by the capability gates.
	r.Route("/api/v1", func(api chi.Router) {
		if cfg.OpenAPIPath != "" {
			api.Get("/openapi.yaml", serveFile(cfg.OpenAPIPath, "application/yaml"))
		}

		api.Group(func(authed chi.Router) {
			if cfg.Auth != nil {
				authed.Use(cfg.Auth)
			}
			if cfg.Limiter != nil {
				authed.Use(middleware.RateLimit(cfg.Limiter, nil))
			}

			mountAPIRoutes(authed, h)
		})
	})

	// Code-first operations (huma, D11): resources are migrating off the
	// hand-mounted chi routes above to typed huma operations whose Go structs
	// generate docs/openapi.yaml. huma operations declare their own full
	// "/api/v1/…" paths (matching the generated spec), so they mount on the ROOT
	// router rather than inside the chi.Route("/api/v1") group — otherwise chi
	// would prepend a second "/api/v1". The same auth + rate-limit middleware is
	// applied here so the assertion auth still runs first. Converted resources are
	// removed from mountAPIRoutes.
	r.Group(func(authed chi.Router) {
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

// mountAPIRoutes wires every §11 resource onto the authenticated group, each
// behind its authz capability gate.
func mountAPIRoutes(r chi.Router, h *handlers.Handlers) {
	// API-key management (create/list/revoke/rotate) lives in the frontend's
	// better-auth api-key plugin (§6); the gateway only verifies keys. No /keys
	// routes here.

	// --- Webhooks (manage) --- converted to huma operations (RegisterWebhookOps);
	// see NewRouter. Intentionally not mounted here.

	// --- Admin (super-admin; cross-organization oversight, §4.3) --- converted to
	// huma operations (RegisterAdminOps); see NewRouter. Intentionally not mounted here.

	// --- Sessions (manage) --- converted to huma operations (RegisterSessionOps);
	// see NewRouter. Intentionally not mounted here.

	// --- Backup import (/backfill, manage) --- converted to huma — see RegisterBackupOps

	// --- Messages (send) --- converted to huma operations (RegisterMessageOps);
	// see NewRouter. Intentionally not mounted here.

	// --- Chats (read for GET, send for mutations) --- converted to huma — see RegisterChatOps

	// --- Contacts (read for GET, send for mutations) --- converted to huma — see RegisterContactOps

	// --- Groups (read for GET, send for mutations) --- converted to huma — see RegisterGroupOps

	// --- Channels (send) --- converted to huma — see RegisterChannelOps

	// --- Status / Presence (send) --- converted to huma — see RegisterStatusOps
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
