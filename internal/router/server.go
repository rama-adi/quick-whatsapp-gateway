// Package router implements the API's public HTTP surface: the single front
// door and trust boundary in front of the WhatsApp gateways (docs/specs/router.md).
// Callers use one base URL + their better-auth credential; the API authenticates
// them and serves every operation locally — REST handlers mount directly, live
// work executes through private engine gRPC, and no reverse proxy remains. The
// API also owns the control-bus subscriber and the realtime WebSocket endpoint.
package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	handlersapi "github.com/rama-adi/quick-whatsapp-gateway/internal/http/handlers"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/http/middleware"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/httpx"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/humax"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/oidp"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/stream"
)

// SessionResolver is the slice of the session repository the realtime ticket
// authorization needs: look up a session to resolve its owning organization.
type SessionResolver interface {
	Get(ctx context.Context, id string) (domain.WASession, error)
}

// middlewareTokenVerifier and middlewareKeyVerifier are the two-acceptor
// authn verifiers (better-auth JWT + api-key). authz.TokenVerifier /
// authz.KeyVerifier satisfy them; the aliases keep this file's signature
// readable without widening the import surface.
type (
	middlewareTokenVerifier = authz.TokenVerifier
	middlewareKeyVerifier   = authz.KeyVerifier
)

func authzAuthenticate(tokens middlewareTokenVerifier, keys middlewareKeyVerifier) func(http.Handler) http.Handler {
	return authz.Authenticate(tokens, keys)
}

func authzCORS(origins []string) func(http.Handler) http.Handler {
	return authz.CORS(origins)
}

// Config wires the API HTTP surface's trust, realtime, and observability
// collaborators. Tokens/Keys are the better-auth verifiers behind the
// two-acceptor authn. Handler groups are optional: a nil group disables its
// route family.
type Config struct {
	Tokens middlewareTokenVerifier
	Keys   middlewareKeyVerifier

	// Sessions resolves session ownership for realtime ticket authorization
	// (scope=session). Required when Redis/Pump are configured.
	Sessions SessionResolver

	CORSOrigins []string
	Readiness   func() error
	OpenAPIPath string // served at /api/v1/openapi.yaml; empty disables

	// Realtime: when Redis + Pump are present the API serves the single WebSocket
	// endpoint + ticket mint; Registry lets the control bus drop live connections
	// on revocation. PublicURL builds the wss:// ticket URL.
	Redis         realtimeRedis
	Pump          *stream.Pump
	Registry      *stream.ConnRegistry
	RedisPrefix   string
	PublicURL     string
	OIDCIssuer    string
	OIDPSigner    *oidp.Signer
	OIDPProvider  *oidp.Provider
	OAuthHandlers *handlersapi.Handlers
	AdminHandlers *handlersapi.Handlers // super-admin operations
	// MessageHandlers serves the message/session operations locally.
	MessageHandlers *handlersapi.Handlers
	// ResourceHandlers serves the projection/stub resource operations locally:
	// webhooks, chats, contacts, groups, channels, status, backup, and admin.
	ResourceHandlers *handlersapi.Handlers

	Now     func() time.Time // optional; nil => time.Now
	DBStats func() sql.DBStats
	Log     *slog.Logger
}

// Server is the immutable, concurrency-safe API HTTP application after
// composition. Request handlers read its collaborators but do not mutate
// configuration. Mutable dependencies such as Redis, repositories, and
// registries own their own synchronization.
type Server struct {
	tokens           middlewareTokenVerifier
	keys             middlewareKeyVerifier
	sessions         SessionResolver
	corsOrigins      []string
	readiness        func() error
	openAPIPath      string
	redis            realtimeRedis
	pump             *stream.Pump
	registry         *stream.ConnRegistry
	redisPrefix      string
	publicURL        string
	oidcIssuer       string
	oidpSigner       *oidp.Signer
	oidpProvider     *oidp.Provider
	oauthHandlers    *handlersapi.Handlers
	adminHandlers    *handlersapi.Handlers
	messageHandlers  *handlersapi.Handlers
	resourceHandlers *handlersapi.Handlers
	wsOrigins        []string
	now              func() time.Time
	dbStats          func() sql.DBStats
	log              *slog.Logger
}

// NewServer validates mandatory dependencies and installs clock and logger
// defaults. It performs no network I/O and either returns a fully usable Server
// or an error; callers must not attempt to serve a partial configuration.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Tokens == nil || cfg.Keys == nil {
		return nil, errMissing("token/key verifiers")
	}
	if (cfg.Redis != nil || cfg.Pump != nil) && cfg.Sessions == nil {
		return nil, errMissing("session resolver for realtime authorization")
	}
	s := &Server{
		tokens:           cfg.Tokens,
		keys:             cfg.Keys,
		sessions:         cfg.Sessions,
		corsOrigins:      cfg.CORSOrigins,
		readiness:        cfg.Readiness,
		openAPIPath:      cfg.OpenAPIPath,
		redis:            cfg.Redis,
		pump:             cfg.Pump,
		registry:         cfg.Registry,
		redisPrefix:      cfg.RedisPrefix,
		publicURL:        cfg.PublicURL,
		oidcIssuer:       strings.TrimRight(cfg.OIDCIssuer, "/"),
		oidpSigner:       cfg.OIDPSigner,
		oidpProvider:     cfg.OIDPProvider,
		oauthHandlers:    cfg.OAuthHandlers,
		adminHandlers:    cfg.AdminHandlers,
		messageHandlers:  cfg.MessageHandlers,
		resourceHandlers: cfg.ResourceHandlers,
		wsOrigins:        cfg.CORSOrigins,
		now:              cfg.Now,
		dbStats:          cfg.DBStats,
		log:              cfg.Log,
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

// Handler builds the API's chi handler. The edge stack (recover/request-id/
// logger/CORS) wraps everything; health/metrics are unauthenticated; every
// /api/v1 surface authenticates once (two-acceptor authn) and is served
// locally by mounted huma operations.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recover(s.log))
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger(s.log, middleware.LoggerOptions{Service: "router", DBStats: s.dbStats}))
	if len(s.corsOrigins) > 0 {
		r.Use(authzCORS(s.corsOrigins))
	}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", s.handleReadyz)
	r.Handle("/metrics", promhttp.Handler())
	if s.oauthHandlers != nil {
		r.Group(func(authed chi.Router) {
			authed.Use(authzAuthenticate(s.tokens, s.keys))
			hapi := humax.NewAPI(authed)
			handlersapi.RegisterOAuthAppOps(hapi, s.oauthHandlers)
		})
	}
	if s.adminHandlers != nil {
		r.Group(func(authed chi.Router) {
			authed.Use(authzAuthenticate(s.tokens, s.keys))
			hapi := humax.NewAPI(authed)
			if s.adminHandlers.GatewayAdmin != nil {
				handlersapi.RegisterGatewayAdminOps(hapi, s.adminHandlers)
			}
		})
	}
	if s.messageHandlers != nil {
		r.Group(func(authed chi.Router) {
			authed.Use(authzAuthenticate(s.tokens, s.keys))
			hapi := humax.NewAPI(authed)
			handlersapi.RegisterMessageOps(hapi, s.messageHandlers)
			handlersapi.RegisterSessionOps(hapi, s.messageHandlers)
		})
	}
	if s.resourceHandlers != nil {
		r.Group(func(authed chi.Router) {
			authed.Use(authzAuthenticate(s.tokens, s.keys))
			hapi := humax.NewAPI(authed)
			handlersapi.RegisterWebhookOps(hapi, s.resourceHandlers)
			handlersapi.RegisterChatOps(hapi, s.resourceHandlers)
			handlersapi.RegisterContactOps(hapi, s.resourceHandlers)
			handlersapi.RegisterGroupOps(hapi, s.resourceHandlers)
			handlersapi.RegisterChannelOps(hapi, s.resourceHandlers)
			handlersapi.RegisterStatusOps(hapi, s.resourceHandlers)
			handlersapi.RegisterBackupOps(hapi, s.resourceHandlers)
			handlersapi.RegisterAdminOps(hapi, s.resourceHandlers)
		})
	}
	if s.oidpSigner != nil {
		r.Get("/.well-known/openid-configuration", s.handleOIDCDiscovery)
		r.Get("/.well-known/oauth-authorization-server", s.handleOIDCDiscovery)
		r.Get("/.well-known/oauth-jwks.json", s.handleOIDCJWKS)
	}
	if s.oidpProvider != nil {
		s.oidpProvider.Mount(r)
	}

	r.Route("/api/v1", func(api chi.Router) {
		if s.openAPIPath != "" {
			api.Get("/openapi.yaml", s.serveFile(s.openAPIPath, "application/yaml"))
		}
		// Realtime WebSocket redeem is authenticated by its single-use ticket, not a
		// bearer (a browser WS cannot set Authorization), so it sits OUTSIDE the
		// bearer-authn group. The ticket mint below is bearer-authenticated.
		api.Get("/realtime", s.handleRealtimeWS)

		api.Group(func(authed chi.Router) {
			authed.Use(authzAuthenticate(s.tokens, s.keys))
			authed.Post("/realtime/ticket", s.handleTicketMint)
		})
	})

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, domain.ErrNotFound("route not found"))
	})
	return r
}

func (s *Server) handleOIDCDiscovery(w http.ResponseWriter, _ *http.Request) {
	issuer := s.oidcIssuer
	if issuer == "" {
		issuer = strings.TrimRight(s.publicURL, "/")
	}
	body := map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"userinfo_endpoint":                     issuer + "/oauth/userinfo",
		"revocation_endpoint":                   issuer + "/oauth/revoke",
		"jwks_uri":                              issuer + "/.well-known/oauth-jwks.json",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"id_token_signing_alg_values_supported": []string{"EdDSA"},
		"subject_types_supported":               []string{"public"},
		"acr_values_supported":                  []string{"wa:dm", "wa:group"},
		"scopes_supported":                      []string{"openid", "profile", "phone", "wa:group", "offline_access"},
		"claims_supported": []string{
			"sub", "acr", "amr", "auth_time", "name", "phone_number", "phone_number_verified",
			"wa_jid", "wa_group_verified", "wa_group_id", "wa_group_name",
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) handleOIDCJWKS(w http.ResponseWriter, r *http.Request) {
	jwks, err := s.oidpSigner.JWKS(r.Context())
	if err != nil {
		httpx.WriteError(w, domain.ErrInternal("oidc jwks unavailable"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(jwks)
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
