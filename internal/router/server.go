// Package router implements the central router: the single front door and trust
// boundary in front of the WhatsApp gateways (docs/specs/router.md). Callers use
// one base URL + their better-auth credential + a session id; the router
// authenticates them, resolves which gateway owns the session, and reverse-proxies
// the request under a short-lived, request-bound Ed25519 assertion the gateway
// trusts (internal/assertion). The router also owns the control-bus subscriber and
// (Increment B) the single realtime WebSocket endpoint.
package router

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/assertion"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/middleware"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// defaultStaleAfter is how long after a gateway's last heartbeat the router treats
// it as unreachable (≈ several missed heartbeats). Derived, not stored (D8).
const defaultStaleAfter = 90 * time.Second

// JWKSPath is where the router publishes its assertion-verification public keys.
const JWKSPath = "/.well-known/router-jwks.json"

// Config wires the router's collaborators from the composition root.
type Config struct {
	Sessions SessionResolver
	Gateways GatewayResolver
	Minter   *assertion.Minter

	// Tokens/Keys are the better-auth verifiers behind the two-acceptor authn that
	// now runs ONLY here (D2). Same authz.Authenticate the gateway used to mount.
	Tokens authz.TokenVerifier
	Keys   authz.KeyVerifier

	CORSOrigins []string
	Readiness   func() error
	OpenAPIPath string // served at /api/v1/openapi.yaml; empty disables

	StaleAfter time.Duration     // optional; <=0 => defaultStaleAfter
	Transport  http.RoundTripper // optional; nil => http.DefaultTransport
	Now        func() time.Time  // optional; nil => time.Now
	Log        *slog.Logger
}

// Server is the router's composed HTTP application.
type Server struct {
	sessions    SessionResolver
	gateways    GatewayResolver
	minter      *assertion.Minter
	tokens      authz.TokenVerifier
	keys        authz.KeyVerifier
	corsOrigins []string
	readiness   func() error
	openAPIPath string
	jwksJSON    []byte
	staleAfter  time.Duration
	transport   http.RoundTripper
	now         func() time.Time
	log         *slog.Logger
}

// NewServer builds a router Server, precomputing the published JWKS.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Sessions == nil || cfg.Gateways == nil {
		return nil, errMissing("session/gateway resolvers")
	}
	if cfg.Minter == nil {
		return nil, errMissing("assertion minter")
	}
	set, err := cfg.Minter.JWKS()
	if err != nil {
		return nil, err
	}
	jwksJSON, err := json.Marshal(set)
	if err != nil {
		return nil, err
	}
	s := &Server{
		sessions:    cfg.Sessions,
		gateways:    cfg.Gateways,
		minter:      cfg.Minter,
		tokens:      cfg.Tokens,
		keys:        cfg.Keys,
		corsOrigins: cfg.CORSOrigins,
		readiness:   cfg.Readiness,
		openAPIPath: cfg.OpenAPIPath,
		jwksJSON:    jwksJSON,
		staleAfter:  cfg.StaleAfter,
		transport:   cfg.Transport,
		now:         cfg.Now,
		log:         cfg.Log,
	}
	if s.staleAfter <= 0 {
		s.staleAfter = defaultStaleAfter
	}
	if s.transport == nil {
		s.transport = http.DefaultTransport
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s, nil
}

func errMissing(what string) error { return &configError{what} }

type configError struct{ what string }

func (e *configError) Error() string { return "router: missing " + e.what }

// Handler builds the router's chi handler. The edge stack (recover/request-id/
// logger/CORS) wraps everything; health/metrics are unauthenticated; the entire
// /api/v1 surface is authenticated once (two-acceptor authn) and then brokered to
// the owning gateway.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recover(s.log))
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger(s.log))
	if len(s.corsOrigins) > 0 {
		r.Use(authz.CORS(s.corsOrigins))
	}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", s.handleReadyz)
	r.Get(JWKSPath, s.handleJWKS)

	r.Route("/api/v1", func(api chi.Router) {
		if s.openAPIPath != "" {
			api.Get("/openapi.yaml", s.serveFile(s.openAPIPath, "application/yaml"))
		}
		api.Group(func(authed chi.Router) {
			authed.Use(authz.Authenticate(s.tokens, s.keys))
			authed.Handle("/*", http.HandlerFunc(s.handleProxy))
		})
	})

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, domain.ErrNotFound("route not found"))
	})
	return r
}

// handleProxy classifies the request and brokers it to the right gateway:
//   - POST /api/v1/sessions            → placement (least-loaded active gateway)
//   - any path naming a specific session → that session's owning gateway
//   - everything else (org-scoped reads/writes on shared MySQL) → any active gateway
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	p := authz.FromContext(r.Context())
	if p == nil {
		httpx.WriteError(w, domain.ErrUnauthorized("authentication required"))
		return
	}

	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions" {
		g, err := s.pickPlacementGateway(r.Context())
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		s.broker(w, r, g, "")
		return
	}

	if session := sessionFromPath(r.URL.Path); session != "" {
		g, err := s.resolveSessionGateway(r.Context(), p, session)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		s.broker(w, r, g, session)
		return
	}

	g, err := s.pickAnyActiveGateway(r.Context())
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	s.broker(w, r, g, "")
}

// sessionFromPath extracts the concrete session id a path targets, or "" if the
// path is not session-scoped. It recognizes ".../sessions/<id>..." anywhere in the
// path (so both /sessions/<id>/... and /admin/sessions/<id>:action route to the
// owner) and strips a trailing :action suffix. The bare collection
// "/api/v1/sessions" has no following segment and yields "".
func sessionFromPath(path string) string {
	const marker = "/sessions/"
	i := strings.Index(path, marker)
	if i < 0 {
		return ""
	}
	seg := path[i+len(marker):]
	if j := strings.IndexByte(seg, '/'); j >= 0 {
		seg = seg[:j]
	}
	if k := strings.IndexByte(seg, ':'); k >= 0 {
		seg = seg[:k]
	}
	return seg
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if s.readiness != nil {
		if err := s.readiness(); err != nil {
			httpx.WriteError(w, domain.ErrUnavailable("not ready: "+err.Error()))
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

func (s *Server) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(s.jwksJSON)
}

func (s *Server) serveFile(path, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		b, err := os.ReadFile(path)
		if err != nil {
			httpx.WriteError(w, domain.ErrNotFound("file not found"))
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(b)
	}
}
