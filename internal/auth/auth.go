// Package auth wires Authula (plugins, RBAC glue) and bootstraps the super-admin.
//
// Layout:
//   - config.go    : ENV-shaped Config + pure normalization/gating logic.
//   - rbac.go      : the role/permission KEY SET the gateway seeds & gates on.
//   - ports.go     : consumer-defined interfaces (RBACStore, TenantStore, ...).
//   - seed.go      : SeedRBAC — idempotent role/permission seeding.
//   - bootstrap.go : BootstrapAdmin — idempotent super-admin provisioning.
//   - tenant.go    : TenantSyncer.SyncTenant — app-side tenants mirror.
//   - plugins.go   : pure plugin-assembly description (testable without a DB).
//   - auth.go      : Build (constructs *authula.Auth, PANICS on error) + the
//     request accessors / middleware / Require helpers.
//   - adapters.go  : maps the live Authula plugin APIs to the ports above.
//
// Everything except Build and the request accessors is DB-free and unit-tested.
package auth

import (
	"context"
	"fmt"
	"net/http"

	authula "github.com/Authula/authula"
	"github.com/Authula/authula/config"
	"github.com/Authula/authula/models"
	accesscontrol "github.com/Authula/authula/plugins/access-control"
	emailpassword "github.com/Authula/authula/plugins/email-password"
	"github.com/Authula/authula/plugins/session"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/mysqldialect"
)

// Auth is the gateway's handle on a constructed Authula instance plus the plugin
// instances whose .Api / middleware we reach for after construction. It is the
// composition root's single auth dependency (constructor injection, no globals).
type Auth struct {
	cfg Config

	auth      *authula.Auth
	session   *session.SessionPlugin
	emailPass *emailpassword.EmailPasswordPlugin
	access    *accesscontrol.AccessControlPlugin

	// rbac / dir / signup are the ports backed by the live plugin APIs; exposed
	// via accessors so SeedRBAC/BootstrapAdmin can be called post-Build.
	rbac   RBACStore
	dir    UserDirectory
	signup SignUpStore

	// resolveTenantID extracts the actor's user id from a request. It defaults to
	// CurrentTenantID (which reads Authula's actor) but is a field so the Require*
	// guards can be unit-tested without constructing a live *authula.Auth.
	resolveTenantID func(*http.Request) (string, bool)
}

// Build constructs the Authula instance with all gateway plugins enabled and
// returns the wrapper. It PANICS if construction fails, because authula.New
// itself panics on any init error (recon §1/§10) — there is no partial-success
// state to recover into. Call SeedRBAC + BootstrapAdmin afterwards.
func Build(cfg Config) (*Auth, error) {
	norm, err := cfg.normalized()
	if err != nil {
		return nil, err
	}

	plan := buildPluginPlan(norm)

	// Instantiate the plugin structs we keep references to.
	sessionPlugin := session.New(plan.sessionConfig())
	emailPlugin := emailpassword.New(plan.emailPasswordConfig())
	accessPlugin := accesscontrol.New(plan.accessControlConfig())

	plugins := plan.plugins(sessionPlugin, emailPlugin, accessPlugin)

	authCfg := config.NewConfig(
		config.WithAppName(norm.AppName),
		config.WithBaseURL(norm.BaseURL),
		config.WithBasePath(norm.BasePath),
		config.WithSecret(norm.Secret),
		config.WithDatabase(models.DatabaseConfig{
			Provider: "mysql",
			URL:      norm.MySQLDSN,
		}),
		config.WithRouteMappings(adminRouteMappings(norm.BasePath)),
	)

	// Open Authula's pool ourselves on the alias-fixing MySQL shim driver (see
	// mysqldriver.go) and inject it via AuthConfig.DB, so its bun migrator's
	// `INSERT ... AS schema_migration` records succeed on real MySQL. Provider
	// stays "mysql" so Authula still selects the MySQL migration set.
	sqlDB, err := openAuthulaMySQL(norm.MySQLDSN)
	if err != nil {
		return nil, fmt.Errorf("auth: open mysql: %w", err)
	}
	bunDB := bun.NewDB(sqlDB, mysqldialect.New())

	// authula.New runs migrations + plugin Init synchronously and panics on error.
	instance := authula.New(&authula.AuthConfig{Config: authCfg, Plugins: plugins, DB: bunDB})

	a := &Auth{
		cfg:       norm,
		auth:      instance,
		session:   sessionPlugin,
		emailPass: emailPlugin,
		access:    accessPlugin,
	}
	// Plugin .Api fields are populated during authula.New's Init phase.
	a.rbac = newRBACAdapter(accessPlugin.Api)
	a.dir = newUserDirectory(instance.CoreServices())
	a.signup = newSignUpAdapter(emailPlugin.Api)
	a.resolveTenantID = a.CurrentTenantID
	return a, nil
}

// Handler returns Authula's HTTP handler, serving everything under BasePath (§2).
func (a *Auth) Handler() http.Handler { return a.auth.Handler() }

// RBAC / Users / SignUp expose the ports backed by the live plugin APIs, so the
// composition root can run SeedRBAC(ctx, a.RBAC()) and
// BootstrapAdmin(ctx, a.Users(), a.SignUp(), a.RBAC(), ...).
func (a *Auth) RBAC() RBACStore      { return a.rbac }
func (a *Auth) Users() UserDirectory { return a.dir }
func (a *Auth) SignUp() SignUpStore  { return a.signup }
func (a *Auth) BasePath() string     { return a.cfg.BasePath }
func (a *Auth) SignUpEnabled() bool  { return a.cfg.signUpEnabled() }

// CookieAuthMiddleware is the session plugin's AuthMiddleware: 401s requests
// without a valid cookie session and populates the actor (recon §5). Use it on
// dashboard routes that require login.
func (a *Auth) CookieAuthMiddleware() func(http.Handler) http.Handler {
	return a.session.AuthMiddleware()
}

// OptionalCookieAuth populates the actor if a cookie session is present but never
// 401s (recon §5). Use it where login is optional (e.g. the NDJSON stream that
// also accepts API keys).
func (a *Auth) OptionalCookieAuth() func(http.Handler) http.Handler {
	return a.session.OptionalAuthMiddleware()
}

// ActorFromRequest wraps auth.GetActorFromRequest (recon §5).
func (a *Auth) ActorFromRequest(r *http.Request) (*models.Actor, bool) {
	return a.auth.GetActorFromRequest(r)
}

// CurrentTenantID derives the tenant id (= Authula user id, §5/§10) from the
// request actor. Returns "" when unauthenticated or when the actor is a machine
// credential without a user id. The boolean reports whether a tenant was found.
func (a *Auth) CurrentTenantID(r *http.Request) (string, bool) {
	actor, ok := a.ActorFromRequest(r)
	if !ok || actor == nil || actor.ID == "" {
		return "", false
	}
	return actor.ID, true
}

// RequireRole returns middleware that 403s unless the request actor holds the
// named role. It reads roles live from the access-control store, so it reflects
// role changes without a restart. Unauthenticated -> 401.
//
// NOTE: for permission-gated Authula-owned routes, prefer declarative
// RouteMappings (see adminRouteMappings) which run inside Authula's enforce hook.
// RequireRole/RequirePermission are for your OWN chi routes mounted outside
// Authula's router.
func (a *Auth) RequireRole(role string) func(http.Handler) http.Handler {
	return a.guard(func(ctx context.Context, userID string) (bool, error) {
		names, err := a.rbac.GetUserRoleNames(ctx, userID)
		if err != nil {
			return false, err
		}
		for _, n := range names {
			if n == role {
				return true, nil
			}
		}
		return false, nil
	})
}

// RequirePermission returns middleware that 403s unless the request actor holds
// the given permission key (via any of its roles). Unauthenticated -> 401.
func (a *Auth) RequirePermission(permKey string) func(http.Handler) http.Handler {
	return a.guard(func(ctx context.Context, userID string) (bool, error) {
		return a.rbac.UserHasPermission(ctx, userID, permKey)
	})
}

// guard is the shared 401/403 wrapper for RequireRole/RequirePermission.
func (a *Auth) guard(allow func(ctx context.Context, userID string) (bool, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resolve := a.resolveTenantID
			if resolve == nil {
				resolve = a.CurrentTenantID
			}
			userID, ok := resolve(r)
			if !ok {
				writeAuthError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			permitted, err := allow(r.Context(), userID)
			if err != nil {
				writeAuthError(w, http.StatusInternalServerError, "authorization check failed")
				return
			}
			if !permitted {
				writeAuthError(w, http.StatusForbidden, "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Close releases Authula plugin + system resources (recon §10).
func (a *Auth) Close() error {
	if err := a.auth.ClosePlugins(); err != nil {
		return err
	}
	return a.auth.CloseSystems()
}
