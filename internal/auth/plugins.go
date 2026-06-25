package auth

import (
	"github.com/Authula/authula/models"
	accesscontrol "github.com/Authula/authula/plugins/access-control"
	actypes "github.com/Authula/authula/plugins/access-control/types"
	"github.com/Authula/authula/plugins/admin"
	admintypes "github.com/Authula/authula/plugins/admin/types"
	"github.com/Authula/authula/plugins/csrf"
	emailpassword "github.com/Authula/authula/plugins/email-password"
	eptypes "github.com/Authula/authula/plugins/email-password/types"
	ratelimit "github.com/Authula/authula/plugins/rate-limit"
	rltypes "github.com/Authula/authula/plugins/rate-limit/types"
	secondarystorage "github.com/Authula/authula/plugins/secondary-storage"
	"github.com/Authula/authula/plugins/session"
	totp "github.com/Authula/authula/plugins/totp"
	totptypes "github.com/Authula/authula/plugins/totp/types"
)

// pluginPlan is the pure, DB-free description of the plugin set the gateway
// enables (§10). It is derived from Config by buildPluginPlan and is what the
// unit tests assert against — so we can verify "all eight plugins enabled, sign-up
// gated by USER_PANEL_ENABLED, Redis configured" without ever calling authula.New.
type pluginPlan struct {
	redisURL      string
	signUpEnabled bool
}

// buildPluginPlan turns a (normalized) Config into the plugin plan. Pure.
func buildPluginPlan(cfg Config) pluginPlan {
	return pluginPlan{
		redisURL:      cfg.RedisURL,
		signUpEnabled: cfg.signUpEnabled(),
	}
}

// --- per-plugin config builders (pure) ---

func (p pluginPlan) secondaryStorageConfig() secondarystorage.SecondaryStoragePluginConfig {
	// Redis secondary storage (§10/§4). REDIS_URL env overrides this URL inside
	// the plugin; if Redis is unreachable it falls back to in-memory (recon §4).
	return secondarystorage.SecondaryStoragePluginConfig{
		Enabled:  true,
		Provider: secondarystorage.SecondaryStorageProviderRedis,
		Redis:    &secondarystorage.RedisStorageConfig{URL: p.redisURL},
	}
}

func (p pluginPlan) rateLimitConfig() rltypes.RateLimitPluginConfig {
	// Protect auth endpoints (§10). Use the Redis provider so limits are shared
	// across the secondary-storage Redis in a multi-process future.
	return rltypes.RateLimitPluginConfig{
		Enabled:  true,
		Provider: rltypes.RateLimitProviderRedis,
	}
}

func (p pluginPlan) sessionConfig() session.SessionPluginConfig {
	// Dashboard cookie sessions (§10). Behavior comes from Config.Session.
	return session.SessionPluginConfig{Enabled: true}
}

func (p pluginPlan) csrfConfig() csrf.CSRFPluginConfig {
	// CSRF protection for cookie routes (§10).
	return csrf.CSRFPluginConfig{Enabled: true}
}

// emailPasswordConfig gates self-registration behind USER_PANEL_ENABLED (§10/§12):
// when the user panel is off, DisableSignUp=true so only the bootstrapped admin
// can authenticate. Login is unaffected.
func (p pluginPlan) emailPasswordConfig() eptypes.EmailPasswordPluginConfig {
	return eptypes.EmailPasswordPluginConfig{
		Enabled:       true,
		AutoSignIn:    true,
		DisableSignUp: !p.signUpEnabled,
	}
}

func (p pluginPlan) totpConfig() totptypes.TOTPPluginConfig {
	// Optional 2FA (§10).
	return totptypes.TOTPPluginConfig{Enabled: true}
}

func (p pluginPlan) accessControlConfig() actypes.AccessControlPluginConfig {
	// Generic RBAC (§10); roles/permissions seeded by SeedRBAC.
	return actypes.AccessControlPluginConfig{Enabled: true}
}

func (p pluginPlan) adminConfig() admintypes.AdminPluginConfig {
	// User/ban/impersonation admin (§10, recon §8).
	return admintypes.AdminPluginConfig{Enabled: true}
}

// plugins assembles the full ordered plugin slice. The session/email-password/
// access-control instances are passed in because the caller keeps references to
// their .Api / middleware; the rest are instantiated here.
func (p pluginPlan) plugins(
	sessionPlugin *session.SessionPlugin,
	emailPlugin *emailpassword.EmailPasswordPlugin,
	accessPlugin *accesscontrol.AccessControlPlugin,
) []models.Plugin {
	return []models.Plugin{
		secondarystorage.New(p.secondaryStorageConfig()),
		ratelimit.New(p.rateLimitConfig()),
		sessionPlugin,
		csrf.New(p.csrfConfig()),
		emailPlugin,
		totp.New(p.totpConfig()),
		accessPlugin,
		admin.New(p.adminConfig()),
	}
}

// adminRouteMappings declares the access-control enforce hook over the admin
// surface (recon §6/§8): the admin plugin's /admin/* routes require admin.access.
// Authula itself prepends BasePath to each mapping path (ConvertRouteMetadata +
// ApplyBasePathToMetadataKey), so we pass the UN-prefixed "/admin/*" and let it
// resolve to "<basePath>/admin/*". basePath is accepted for symmetry/future use.
func adminRouteMappings(_ string) []models.RouteMapping {
	return []models.RouteMapping{
		{
			Paths:       []string{"/admin/*"},
			Plugins:     []string{"access_control.enforce"},
			Permissions: []string{PermAdminAccess},
		},
	}
}
