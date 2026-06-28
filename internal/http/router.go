package httpx

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/middleware"
	shttpx "github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
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

			// The live event stream: any authenticated caller with the events
			// capability (api-key events permission, or any JWT role) (§11).
			authed.Group(func(ev chi.Router) {
				ev.Use(authz.RequireEvents())
				ev.Get("/events", h.Events)
			})

			mountAPIRoutes(authed, h)
		})
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

	// --- Webhooks (manage) ---
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireManage())
		g.Post("/webhooks", h.CreateWebhook)
		g.Get("/webhooks", h.ListWebhooks)
		g.Get("/webhooks/{id}", h.GetWebhook)
		g.Patch("/webhooks/{id}", h.UpdateWebhook)
		g.Delete("/webhooks/{id}", h.DeleteWebhook)
	})

	// --- Admin (super-admin; cross-organization oversight, §4.3) ---
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireSuperAdmin())
		g.Get("/admin/sessions", h.AdminListSessions)
		g.Post("/admin/sessions/{session}:backfill", h.AdminStartSessionBackfill)
		g.Get("/admin/sessions/{session}/backfill", h.AdminSessionBackfillStatus)
	})

	// --- Sessions (manage) ---
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireManage())
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

		// Backup import: upload a WhatsApp msgstore.db.crypt15 to backfill the
		// session's chats/messages/identities/groups. Once per 24h per session for
		// non-admins; super_admins bypass (gates already let super_admin through).
		g.Post("/sessions/{session}/backfill", h.ImportBackup)
		g.Get("/sessions/{session}/backfill", h.BackupStatus)
	})

	// --- Messages (send) ---
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireSend())
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
		g.Use(authz.RequireRead())
		g.Get("/sessions/{session}/chats", h.ListChats)
		g.Get("/sessions/{session}/chats/{cid}", h.GetChat)
		g.Get("/sessions/{session}/chats/{cid}/messages", h.ListChatMessages)
	})
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireSend())
		g.Post("/sessions/{session}/chats/{cid}/read", h.ReadChat)
		g.Patch("/sessions/{session}/chats/{cid}", h.UpdateChat)
		g.Delete("/sessions/{session}/chats/{cid}", h.DeleteChat)
		g.Put("/sessions/{session}/chats/{cid}/presence", h.ChatPresence)
	})

	// --- Contacts (read for GET, send for mutations) ---
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireRead())
		g.Get("/sessions/{session}/contacts", h.ListContacts)
		g.Get("/sessions/{session}/contacts/check", h.CheckContact)
		g.Get("/sessions/{session}/contacts/{lid}", h.GetContact)
		g.Get("/sessions/{session}/contacts/{jid}/picture", h.ContactPicture)
		g.Get("/sessions/{session}/contacts/{jid}/about", h.ContactAbout)
	})
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireSend())
		g.Post("/sessions/{session}/contacts/{jid}/block", h.BlockContact)
		g.Post("/sessions/{session}/contacts/{jid}/unblock", h.UnblockContact)
	})

	// --- Groups (read for GET, send for mutations) ---
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireRead())
		g.Get("/sessions/{session}/groups", h.ListGroups)
		g.Get("/sessions/{session}/groups/{gid}", h.GetGroup)
		g.Get("/sessions/{session}/groups/{gid}/members", h.ListGroupMembers)
		g.Get("/sessions/{session}/groups/{gid}/invite", h.GetGroupInvite)
	})
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireSend())
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
		g.Use(authz.RequireSend())
		g.Post("/sessions/{session}/channels", h.CreateChannel)
		g.Post("/sessions/{session}/channels/{jid}:follow", h.FollowChannel)
		g.Post("/sessions/{session}/channels/{jid}:unfollow", h.UnfollowChannel)
		g.Post("/sessions/{session}/channels/{jid}:mute", h.MuteChannel)
		g.Get("/sessions/{session}/channels/{jid}/messages", h.ListChannelMessages)
	})

	// --- Status / Presence (send) ---
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireSend())
		g.Post("/sessions/{session}/status", h.PostStatus)
		g.Put("/sessions/{session}/presence", h.SetPresence)
	})
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
