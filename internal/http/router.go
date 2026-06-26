package httpx

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/auth"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/middleware"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/static"
	shttpx "github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// RouterConfig groups everything the router needs from the composition root.
type RouterConfig struct {
	Handlers *handlers.Handlers
	Auth     *auth.Auth

	// Verifier authenticates API keys (the KeyService satisfies it).
	Verifier middleware.APIKeyVerifier
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

// NewRouter builds the full chi router per §11: Authula under /auth, the JSON API
// under /api/v1 (with the middleware stack + per-route auth/permission gates),
// the embedded SPA with index.html fallback, and the health/metrics probes.
func NewRouter(cfg RouterConfig) http.Handler {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	h := cfg.Handlers

	r := chi.NewRouter()
	// Base stack: Recover MUST be outermost so a panic anywhere below is caught.
	r.Use(middleware.Recover(log))
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger(log))

	// Health / readiness / metrics (unauthenticated).
	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz(cfg.Readiness))
	r.Handle("/metrics", promhttp.Handler())

	// Authula auth surface under /auth (login, registration, admin, etc.).
	//
	// Authula's own router already prefixes every route with its BasePath
	// ("/auth"), so its handler expects to see the full "/auth/..." request path.
	// chi's Mount strips the matched prefix before delegating, which would feed
	// Authula "/email-password/sign-in" and 404. We therefore Handle the wildcard
	// (which preserves the full path) instead of Mount-ing it.
	if cfg.Auth != nil {
		base := strings.TrimRight(cfg.Auth.BasePath(), "/")
		r.Handle(base, cfg.Auth.Handler())
		r.Handle(base+"/*", cfg.Auth.Handler())
	}

	// JSON API under /api/v1.
	r.Route("/api/v1", func(api chi.Router) {
		if cfg.OpenAPIPath != "" {
			api.Get("/openapi.yaml", serveFile(cfg.OpenAPIPath, "application/yaml"))
		}

		// The live event stream accepts EITHER an API key OR a dashboard cookie.
		// Enrich with both (cookie first, then api-key), then require a organization.
		api.Group(func(ev chi.Router) {
			if cfg.Auth != nil {
				// internal/auth (Authula) still exposes CurrentTenantID; it resolves
				// the same owner id the cookie enricher lifts as the organization id.
				// internal/auth is removed in a later stage.
				ev.Use(middleware.CookieSession(cfg.Auth.OptionalCookieAuth(), cfg.Auth.CurrentTenantID))
			}
			ev.Use(optionalAPIKey(cfg.Verifier))
			ev.Use(middleware.RequireEvents())
			ev.Get("/events", h.Events)
		})

		// Programmatic API: API-key authenticated, permission-gated.
		api.Group(func(pr chi.Router) {
			pr.Use(middleware.APIKeyAuth(cfg.Verifier))
			if cfg.Limiter != nil {
				pr.Use(middleware.RateLimit(cfg.Limiter, nil))
			}
			mountAPIRoutes(pr, h)
		})
	})

	// SPA: serve the embedded dashboard for all non-API, non-auth paths, with
	// index.html fallback for client-side routing.
	r.NotFound(spaHandler(log))

	return r
}

// mountAPIRoutes wires every §11 resource onto the API-key-authenticated group.
func mountAPIRoutes(r chi.Router, h *handlers.Handlers) {
	// API-key management (create/list/revoke/rotate) lives in the frontend's
	// better-auth api-key plugin (§6); the gateway only verifies keys. No /keys
	// routes here.

	// --- Webhooks (manage) ---
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireManage())
		g.Post("/webhooks", h.CreateWebhook)
		g.Get("/webhooks", h.ListWebhooks)
		g.Get("/webhooks/{id}", h.GetWebhook)
		g.Patch("/webhooks/{id}", h.UpdateWebhook)
		g.Delete("/webhooks/{id}", h.DeleteWebhook)
	})

	// --- Admin (manage; cross-organization oversight) ---
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireManage())
		g.Get("/admin/sessions", h.AdminListSessions)
	})

	// --- Sessions (manage) ---
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireManage())
		g.Post("/sessions", h.CreateSession)
		g.Get("/sessions", h.ListSessions)
		g.Get("/sessions/{session}", h.GetSession)
		g.Post("/sessions/{session}:start", h.StartSession)
		g.Post("/sessions/{session}:stop", h.StopSession)
		g.Post("/sessions/{session}:restart", h.RestartSession)
		g.Post("/sessions/{session}:logout", h.LogoutSession)
		g.Delete("/sessions/{session}", h.DeleteSession)
		g.Get("/sessions/{session}/me", h.SessionMe)
		g.Get("/sessions/{session}/qr", h.SessionQR)
		g.Post("/sessions/{session}/pairing-code", h.SessionPairingCode)
	})

	// --- Messages (send) ---
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireSend())
		g.Post("/sessions/{session}/messages", h.SendMessage)
		g.Patch("/sessions/{session}/messages/{mid}", h.EditMessage)
		g.Delete("/sessions/{session}/messages/{mid}", h.RevokeMessage)
		g.Post("/sessions/{session}/messages/{mid}/reaction", h.AddReaction)
		g.Delete("/sessions/{session}/messages/{mid}/reaction", h.RemoveReaction)
		g.Post("/sessions/{session}/messages/{mid}/forward", h.ForwardMessage)
		g.Post("/sessions/{session}/messages/{mid}/vote", h.VoteMessage)
	})

	// --- Chats (read for GET, send for mutations) ---
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireRead())
		g.Get("/sessions/{session}/chats", h.ListChats)
		g.Get("/sessions/{session}/chats/{cid}", h.GetChat)
		g.Get("/sessions/{session}/chats/{cid}/messages", h.ListChatMessages)
	})
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireSend())
		g.Post("/sessions/{session}/chats/{cid}/read", h.ReadChat)
		g.Patch("/sessions/{session}/chats/{cid}", h.UpdateChat)
		g.Delete("/sessions/{session}/chats/{cid}", h.DeleteChat)
		g.Put("/sessions/{session}/chats/{cid}/presence", h.ChatPresence)
	})

	// --- Contacts (read for GET, send for mutations) ---
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireRead())
		g.Get("/sessions/{session}/contacts", h.ListContacts)
		g.Get("/sessions/{session}/contacts/check", h.CheckContact)
		g.Get("/sessions/{session}/contacts/{lid}", h.GetContact)
		g.Get("/sessions/{session}/contacts/{jid}/picture", h.ContactPicture)
		g.Get("/sessions/{session}/contacts/{jid}/about", h.ContactAbout)
	})
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireSend())
		g.Post("/sessions/{session}/contacts/{jid}/block", h.BlockContact)
		g.Post("/sessions/{session}/contacts/{jid}/unblock", h.UnblockContact)
	})

	// --- Groups (read for GET, send for mutations) ---
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireRead())
		g.Get("/sessions/{session}/groups", h.ListGroups)
		g.Get("/sessions/{session}/groups/{gid}", h.GetGroup)
		g.Get("/sessions/{session}/groups/{gid}/members", h.ListGroupMembers)
		g.Get("/sessions/{session}/groups/{gid}/invite", h.GetGroupInvite)
	})
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireSend())
		g.Post("/sessions/{session}/groups", h.CreateGroup)
		g.Post("/sessions/{session}/groups/{gid}/members", h.AddGroupMembers)
		g.Delete("/sessions/{session}/groups/{gid}/members/{jid}", h.RemoveGroupMember)
		g.Post("/sessions/{session}/groups/{gid}/members/{jid}/promote", h.PromoteGroupMember)
		g.Post("/sessions/{session}/groups/{gid}/members/{jid}/demote", h.DemoteGroupMember)
		g.Patch("/sessions/{session}/groups/{gid}", h.UpdateGroup)
		g.Delete("/sessions/{session}/groups/{gid}/invite", h.RevokeGroupInvite)
		g.Post("/sessions/{session}/groups:join", h.JoinGroup)
		g.Post("/sessions/{session}/groups/{gid}:leave", h.LeaveGroup)
		g.Post("/sessions/{session}/groups/{gid}/members:approve", h.ApproveGroupMembers)
	})

	// --- Channels (send) ---
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireSend())
		g.Post("/sessions/{session}/channels", h.CreateChannel)
		g.Post("/sessions/{session}/channels/{jid}:follow", h.FollowChannel)
		g.Post("/sessions/{session}/channels/{jid}:unfollow", h.UnfollowChannel)
		g.Post("/sessions/{session}/channels/{jid}:mute", h.MuteChannel)
		g.Get("/sessions/{session}/channels/{jid}/messages", h.ListChannelMessages)
	})

	// --- Status / Presence (send) ---
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireSend())
		g.Post("/sessions/{session}/status", h.PostStatus)
		g.Put("/sessions/{session}/presence", h.SetPresence)
	})
}

// optionalAPIKey is a non-rejecting API-key enricher used on the events route
// (which also accepts a cookie). It runs the verifier when a bearer key is
// present and, on success, lifts the api-key + organization into context; any failure
// or absence is ignored (the cookie enricher and RequireEvents gate decide).
func optionalAPIKey(verifier middleware.APIKeyVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if verifier == nil {
				next.ServeHTTP(w, r)
				return
			}
			raw, ok := bearer(r)
			if !ok || raw == "" {
				next.ServeHTTP(w, r)
				return
			}
			key, orgID, err := verifier.Verify(r.Context(), raw)
			if err == nil && key != nil {
				ctx := shttpx.SetAPIKey(r.Context(), key)
				if orgID != "" {
					ctx = shttpx.SetOrganizationID(ctx, orgID)
				}
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearer extracts a case-insensitive "Bearer <token>" Authorization header.
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const scheme = "bearer "
	if len(h) < len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(h[len(scheme):]), true
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

// spaHandler serves the embedded SPA build. Static assets are served directly;
// any other path falls back to index.html for client-side routing.
func spaHandler(log *slog.Logger) http.HandlerFunc {
	dist, err := fs.Sub(static.Assets, "dist")
	if err != nil {
		log.Error("spa: failed to open embedded assets", "err", err)
		dist = static.Assets
	}
	fileServer := http.FileServer(http.FS(dist))
	return func(w http.ResponseWriter, r *http.Request) {
		// Never serve the SPA for API/auth paths — those 404 as JSON.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			shttpx.WriteError(w, domain.ErrNotFound("route not found"))
			return
		}
		upath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if upath == "" {
			upath = "index.html"
		}
		if _, err := fs.Stat(dist, upath); err != nil {
			// Unknown path -> index.html fallback (client-side routing).
			serveIndex(w, r, dist)
			return
		}
		fileServer.ServeHTTP(w, r)
	}
}

func serveIndex(w http.ResponseWriter, r *http.Request, dist fs.FS) {
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		shttpx.WriteError(w, domain.ErrNotFound("not found"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}
