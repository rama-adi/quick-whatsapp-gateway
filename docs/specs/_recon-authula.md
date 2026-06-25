# Authula Recon Cheat-Sheet (v1.12.0)

> Source of truth: read directly from the module cache at
> `$(go env GOMODCACHE)/github.com/!authula/authula@v1.12.0/` (the `!authula`
> directory is Go's case-escaping for the capital `A`).
> Verified against the real source on 2026-06-26. Sections marked **UNVERIFIED**
> were not provable from source and include the safest workaround.

---

## 0. CRITICAL — module path & Go version (read this first)

- **The import/module path is `github.com/Authula/authula` with a CAPITAL `A`.**
  `go get github.com/authula/authula` FAILS with:
  `module declares its path as: github.com/Authula/authula`.
  Use `go get github.com/Authula/authula@v1.12.0`.
- **Requires `go >= 1.26.4`** (declared in the module's `go.mod`). Your toolchain
  must support this or `go get`/build will auto-switch toolchains. Make sure the
  project's `go.mod` `go` directive is compatible.
- Root import alias used by the library itself: `authula "github.com/Authula/authula"`.
- Key transitive deps already vendored by Authula: `go-chi/chi/v5 v5.3.0`,
  `uptrace/bun v1.2.18` (+ mysql/pg/sqlite dialects), `redis/go-redis/v9 v9.20.0`,
  drivers `go-sql-driver/mysql`, `lib/pq`, `mattn/go-sqlite3` (CGO sqlite).

### Plugin package import paths + package names (EXACT)
| Plugin | Import path | `package` name |
|---|---|---|
| Email & Password | `github.com/Authula/authula/plugins/email-password` | `email_password` |
| Session | `github.com/Authula/authula/plugins/session` | `session` |
| CSRF | `github.com/Authula/authula/plugins/csrf` | `csrf` |
| TOTP | `github.com/Authula/authula/plugins/totp` | `totp` |
| Access Control (RBAC) | `github.com/Authula/authula/plugins/access-control` | `accesscontrol` |
| Admin | `github.com/Authula/authula/plugins/admin` | `admin` |
| Rate Limit | `github.com/Authula/authula/plugins/rate-limit` | `ratelimit` |
| Secondary Storage | `github.com/Authula/authula/plugins/secondary-storage` | `secondarystorage` |

Note the dir-name vs package-name mismatch (e.g. `plugins/access-control` →
`package accesscontrol`). When importing you may want explicit aliases:
```go
import (
    emailpassword "github.com/Authula/authula/plugins/email-password"
    accesscontrol "github.com/Authula/authula/plugins/access-control"
    secondarystorage "github.com/Authula/authula/plugins/secondary-storage"
    ratelimit "github.com/Authula/authula/plugins/rate-limit"
)
```
Plugin config structs live in sub-packages `.../<plugin>/types` for
email-password, totp, access-control, admin, rate-limit. For session/csrf/
secondary-storage the config struct lives in the plugin package itself.

---

## 1. Constructing / initializing Authula (embedded library mode)

Entry point type: `authula.Auth`. Constructor: `authula.New(*authula.AuthConfig) *Auth`.

```go
type AuthConfig struct {
    Config  *models.Config   // REQUIRED (panics if nil)
    Plugins []models.Plugin  // plugin instances
    DB      bun.IDB          // OPTIONAL: pass your own *bun.DB; if nil Authula opens its own
}
func New(authConfig *AuthConfig) *Auth   // PANICS on any init error (not error-returning!)
```

`New` does ALL of this synchronously and **panics** on failure:
1. inits validator + logger
2. opens DB (if `DB == nil`) from `Config.Database`
3. runs CORE migrations
4. inits event bus
5. builds core services + service registry
6. registers + migrates + inits each ENABLED plugin
7. inits core systems (session/verification cleanup)
8. registers plugin middleware

Build the `*models.Config` with the functional-options helper
`config.NewConfig(...)` from `github.com/Authula/authula/config`:

```go
import (
    "github.com/Authula/authula/config"
    "github.com/Authula/authula/models"
)

cfg := config.NewConfig(
    config.WithAppName("Quick WhatsApp Gateway"),
    config.WithBaseURL("https://gw.example.com"),
    config.WithBasePath("/auth"),                 // default "/auth"
    config.WithSecret(os.Getenv("AUTHULA_SECRET")), // env AUTHULA_SECRET wins over arg
    config.WithDatabase(models.DatabaseConfig{
        Provider: "mysql",                         // "sqlite" | "postgres" | "mysql"
        URL:      "user:pass@tcp(localhost:3306)/authula",
    }),
    config.WithSession(models.SessionConfig{ /* ... */ }),
)
```

Config defaults (from `config.NewConfig`): `BasePath="/auth"`,
`Session.CookieName="authula.session_token"`, `Session.ExpiresIn=7d`,
`Session.HttpOnly=true`, `Session.Secure=false`, `Session.SameSite="lax"`,
CORS `AllowCredentials=true`, `AllowedOrigins=["*"]`.
**Production guard:** if `GO_ENV=production` and the secret is still the default,
`NewConfig` PANICS — always set a real `AUTHULA_SECRET`.

Env-var overrides honored by options: `AUTHULA_BASE_URL`, `AUTHULA_SECRET`,
`AUTHULA_DATABASE_URL` (all override the in-code value).

### Standalone/config-file mode (alternative, not needed for embedding)
`cmd/main.go` builds plugins from config via
`bootstrap.BuildPluginsFromConfig(config)` (pkg
`github.com/Authula/authula/internal/bootstrap`) — but for an embedded library
you instantiate plugins yourself (see §3). Prefer manual instantiation.

---

## 2. Mounting HTTP routes

`auth.Handler() http.Handler` returns a ready `net/http` handler (internally a
**chi v5** router). It is built once (sync.Once) and serves everything under
`Config.BasePath` (default `/auth`).

```go
auth := authula.New(&authula.AuthConfig{Config: cfg, Plugins: plugins})
mux := http.NewServeMux()
mux.Handle("/auth/", auth.Handler())   // Authula owns everything under BasePath
http.ListenAndServe(":8080", mux)
```

> The handler already prefixes plugin routes with `BasePath`. So e.g. the
> email-password sign-in route `/email-password/sign-in` is served at
> `/auth/email-password/sign-in`. **Do NOT double-mount under a second `/auth`.**
> If you set `BasePath="/auth"` and also `mux.Handle("/auth/", ...)`, paths line up.

Useful methods on `*Auth` for integrating your own app routes/middleware:
- `auth.RegisterMiddleware(mw ...func(http.Handler) http.Handler)` — MUST be
  called before `Handler()` (chi requires middleware before routes).
- `auth.RegisterRoute(models.Route)` / `RegisterRoutes([]Route)` — adds routes
  UNDER `BasePath`.
- `auth.RegisterCustomRoute(models.Route)` / `RegisterCustomRoutes` /
  `RegisterCustomRouteGroup(models.RouteGroup)` — adds routes WITHOUT the
  `BasePath` prefix (use these for your dashboard pages).
- `auth.RegisterHook(models.Hook)` / `RegisterHooks`.
- `auth.Router() *authula.Router`, `auth.DB() bun.IDB`,
  `auth.EventBus() models.EventBus`, `auth.CoreServices() *services.CoreServices`,
  `auth.ServiceRegistry` (field), `auth.PluginRegistry` (field).
- `auth.ClosePlugins() error`, `auth.CloseSystems() error` for shutdown.

`models.Route`:
```go
type Route struct {
    Method     string
    Path       string
    Handler    http.Handler
    Middleware []func(http.Handler) http.Handler
    Metadata   map[string]any   // "plugins" + "permissions" keys drive hooks/RBAC
}
type RouteGroup struct { Path string; Routes []Route; Metadata map[string]any }
```

---

## 3. Plugin system — registration + EXACT constructor signatures

All plugins implement `models.Plugin`:
```go
type Plugin interface {
    Metadata() PluginMetadata
    Config() any
    Init(ctx *PluginContext) error
    Close() error
}
```
Optional extra interfaces a plugin may satisfy: `PluginWithRoutes` (`Routes()`),
`PluginWithMiddleware` (`Middleware()`), `PluginWithHooks` (`Hooks()`),
`PluginWithMigrations` (`Migrations(provider) / DependsOn()`),
`AuthMethodProvider` (`AuthMiddleware()/OptionalAuthMiddleware()`).

**Registration = put the instances in `AuthConfig.Plugins`.** `New()` registers
each, but ONLY if the plugin is "enabled". A plugin is enabled when its config
`Enabled` field is true (see `util.IsPluginEnabled`). **Set `Enabled: true` in
every plugin config you pass, or it is silently skipped.**

Plugin IDs (`models.PluginID`): `access_control`, `admin`, `secondary_storage`,
`email`, `csrf`, `email_password`, `oauth2`, `session`, `jwt`, `bearer`,
`ratelimit`, `magic_link`, `totp`, `organizations`.

### Constructor signatures (verified)

```go
// Email & Password
import emailpassword "github.com/Authula/authula/plugins/email-password"
import eptypes "github.com/Authula/authula/plugins/email-password/types"
emailpassword.New(eptypes.EmailPasswordPluginConfig{
    Enabled:                  true,
    RequireEmailVerification: false,
    AutoSignIn:               true,
    // MinPasswordLength default 8, MaxPasswordLength default 128
    // Optional Send*Email func hooks for custom mail (else needs email plugin)
}) *EmailPasswordPlugin

// Session (cookie-based) — config has ONLY Enabled; behavior comes from Config.Session
import "github.com/Authula/authula/plugins/session"
session.New(session.SessionPluginConfig{ Enabled: true }) *SessionPlugin

// CSRF
import "github.com/Authula/authula/plugins/csrf"
csrf.New(csrf.CSRFPluginConfig{
    Enabled: true,
    // CookieName default "authula_csrf_token", HeaderName default "X-AUTHULA-CSRF-TOKEN"
    // MaxAge default 24h, SameSite default "lax", EnableHeaderProtection bool
}) *CSRFPlugin

// TOTP (authenticator app, backup codes, trusted devices)
import "github.com/Authula/authula/plugins/totp"
import totptypes "github.com/Authula/authula/plugins/totp/types"
totp.New(totptypes.TOTPPluginConfig{
    Enabled: true,
    // BackupCodeCount default 10, TrustedDeviceDuration default 30d,
    // PendingTokenExpiry default 5m, SameSite default ...
}) *TOTPPlugin

// Access Control (RBAC) — roles + permissions tables
import accesscontrol "github.com/Authula/authula/plugins/access-control"
import actypes "github.com/Authula/authula/plugins/access-control/types"
accesscontrol.New(actypes.AccessControlPluginConfig{ Enabled: true }) *AccessControlPlugin

// Admin (user/account/session admin CRUD, ban, impersonation)
import "github.com/Authula/authula/plugins/admin"
import admintypes "github.com/Authula/authula/plugins/admin/types"
admin.New(admintypes.AdminPluginConfig{
    Enabled:                   true,
    ImpersonationMaxExpiresIn: 0, // defaults to 15m
}) *AdminPlugin

// Rate Limit
import ratelimit "github.com/Authula/authula/plugins/rate-limit"
import rltypes "github.com/Authula/authula/plugins/rate-limit/types"
ratelimit.New(rltypes.RateLimitPluginConfig{
    Enabled:  true,                              // auto-forced true when GO_ENV=production
    Window:   time.Minute,                       // default 1m
    Max:      100,                               // default 100
    Provider: rltypes.RateLimitProviderInMemory, // "memory" | "redis" | "database"
    // CustomRules map[string]RateLimitRule for per-route overrides
}) *RateLimitPlugin

// Secondary Storage (memory | database | redis)
import secondarystorage "github.com/Authula/authula/plugins/secondary-storage"
secondarystorage.New(secondarystorage.SecondaryStoragePluginConfig{
    Enabled:  true,
    Provider: secondarystorage.SecondaryStorageProviderRedis, // memory|database|redis
    Redis: &secondarystorage.RedisStorageConfig{
        URL:      "redis://localhost:6379/0", // or env REDIS_URL (env WINS)
        PoolSize: 10, MaxRetries: 3,
    },
}) *SecondaryStoragePlugin
// Alt: secondarystorage.NewWithStorage(providerName string, storage models.SecondaryStorage)
//      to inject your own backend.
```

**Assembly example:**
```go
plugins := []models.Plugin{
    secondarystorage.New(secondarystorage.SecondaryStoragePluginConfig{Enabled: true, Provider: secondarystorage.SecondaryStorageProviderRedis, Redis: &secondarystorage.RedisStorageConfig{URL: redisURL}}),
    ratelimit.New(rltypes.RateLimitPluginConfig{Enabled: true}),
    session.New(session.SessionPluginConfig{Enabled: true}),
    csrf.New(csrf.CSRFPluginConfig{Enabled: true}),
    emailpassword.New(eptypes.EmailPasswordPluginConfig{Enabled: true}),
    totp.New(totptypes.TOTPPluginConfig{Enabled: true}),
    accesscontrol.New(actypes.AccessControlPluginConfig{Enabled: true}),
    admin.New(admintypes.AdminPluginConfig{Enabled: true}),
}
auth := authula.New(&authula.AuthConfig{Config: cfg, Plugins: plugins})
```

---

## 4. Storage — MySQL backing + Redis secondary storage

### MySQL (primary DB)
Set in `models.DatabaseConfig`:
- `Provider: "mysql"` (exact string; valid: `"sqlite"`, `"postgres"`, `"mysql"`).
- `URL: "username:password@tcp(host:3306)/dbname"` (go-sql-driver DSN, NO scheme).
  Env `AUTHULA_DATABASE_URL` overrides.
Authula opens it with `database/sql` + `bun` mysqldialect and runs migrations
automatically inside `New()`. You can also pass your own `bun.IDB` via
`AuthConfig.DB` if you want to share a pool with the rest of your app.
Pool knobs: `MaxOpenConns` (default 25), `MaxIdleConns` (5), `ConnMaxLifetime` (10m).

### Redis secondary storage
Configure the secondary-storage plugin with `Provider: "redis"` and
`Redis: &RedisStorageConfig{URL: "redis://..."}`. Env `REDIS_URL` overrides the
config URL. Redis URL format: `redis://[user:pass@]host[:port]/db`.
If Redis init fails, the plugin **silently falls back to in-memory** (logs a warn).
The plugin registers `models.ServiceSecondaryStorage` ("secondary_storage_service")
in the service registry for other plugins (e.g. rate-limit) to consume.
> Rate-limit's own Redis: rate-limit has its own provider enum
> (`RateLimitProviderRedis`) separate from secondary-storage. **UNVERIFIED**
> whether rate-limit auto-uses the secondary-storage Redis; to use Redis for
> rate limiting, set `Provider: RateLimitProviderRedis` explicitly on the
> rate-limit config (its Redis wiring detail was not fully read — verify the
> rate-limit plugin Init if you need Redis-backed limits).

---

## 5. Reading the current authenticated user / session from a request

The session plugin populates the request context. Two layers:

1. **Raw context keys** (set by `SessionPlugin.AuthMiddleware`):
   - `models.ContextUserID` → `session.UserID` (string)
   - `models.ContextSessionID` → `session.ID` (string)
2. **Actor abstraction** (preferred, set by the router during the request
   lifecycle): `models.Actor` carried under `models.ContextAuthActor`.

Accessors (use these — they are convenience wrappers, no `models` import needed):
```go
actor, ok := auth.GetActorFromRequest(r)   // (*models.Actor, bool)
actor, ok := auth.GetActorFromContext(ctx)
// or directly:
actor, ok := models.GetActorFromRequest(r)
actor, ok := models.GetActorFromContext(ctx)
```
`models.Actor`:
```go
type Actor struct {
    ID             string                 // the user/credential ID
    Type           ActorType              // "user" | "machine"
    OrganizationID *string
    Scopes         []string
    Metadata       map[string]any
}
func (a *Actor) HasScopes(scope string) bool
```
For your dashboard cookie-session routes:
- Mount them as custom routes (`auth.RegisterCustomRoute*`) OR your own mux.
- To require a logged-in cookie session, attach the session plugin's
  `AuthMiddleware()`:
  ```go
  sp := session.New(session.SessionPluginConfig{Enabled: true})
  // after auth is built, sp is the same instance you passed in:
  protected := sp.AuthMiddleware()(yourDashboardHandler)
  ```
  `AuthMiddleware` returns 401 JSON `{"message":"unauthorized"}` if no/invalid
  cookie; on success it puts `UserID`/`SessionID` in context. There's also
  `OptionalAuthMiddleware()` (populates if present, never 401s).
- Cookie name comes from `Config.Session.CookieName` (default
  `authula.session_token`). Session plugin also exposes `SetSessionCookie(w,
  token)` / `ClearSessionCookie(w)`.
- The matched route's actor (`reqCtx.Actor`) is the canonical identity inside
  Authula-owned routes; you fetch the full user via `CoreServices().UserService.GetByID(ctx, actor.ID)`.

---

## 6. RBAC — roles, permissions, gating routes

**DISCREPANCY vs masterplan assumption:** Authula RBAC is **NOT** a fixed
`super_admin`/`user` enum. It is a generic **roles + permissions** model with DB
tables: `access_control_roles`, `access_control_permissions`,
`access_control_role_permissions`, `access_control_user_roles`. Roles have a
`Weight` and `IsSystem` flag. There are NO built-in role names like
`super_admin`/`user` shipped — **you create them yourself** (e.g. seed a
`super_admin` role with the permissions you need; see §7). Treat
`super_admin`/`user` as roles YOU define, not library constants.

### How gating works (hook-driven, opt-in via route metadata)
The access-control plugin registers a `HookBefore` hook with PluginID
`access_control.enforce` (constant `HookIDAccessControlEnforce`). For a route,
enforcement runs ONLY if:
- the route metadata `"plugins"` list contains `"access_control.enforce"`, AND
- the route metadata `"permissions"` list is non-empty.

Behavior of the enforce hook:
- If `Actor == nil`/empty ID → 401.
- For `ActorMachine`: checks required permissions against `Actor.Scopes` (all must match).
- For `ActorUser`: calls `API.HasPermissions(ctx, userID, requiredPerms)` against the DB.
- Missing perms → 403 Forbidden. No `permissions` metadata → skipped (opt-in).

### Declaring permission requirements on routes — two ways
1. **Config-driven `RouteMapping`** (recommended; works for any path incl. your
   own dashboard if under BasePath). On the `models.Config`:
   ```go
   config.WithRouteMappings([]models.RouteMapping{
       {
           Paths:       []string{"GET:/admin/*", "/dashboard/*"},
           Plugins:     []string{"access_control.enforce"},
           Permissions: []string{"users.read"},
       },
   })
   ```
   Note: `Paths` are matched after BasePath is applied
   (`ApplyBasePathToMetadataKey`). Use `METHOD:/path` or just `/path` (all methods).
2. **Per-`models.Route` metadata**: set
   `Metadata: map[string]any{"plugins": []string{"access_control.enforce"}, "permissions": []string{"users.write"}}`.

### Programmatic RBAC API
Get the plugin instance's `.Api` (`*accesscontrol.API`) — keep a reference to the
plugin you constructed; `Init` builds `p.Api` and it's accessible via the
exported `Api` field on the plugin struct **after** `auth.New` runs (Init has
executed). Methods (all `ctx context.Context`):
```go
CreateRole(ctx, types.CreateRoleRequest{Name, Description, Weight, IsSystem}) (*types.Role, error)
GetRoleByName(ctx, name) (*types.Role, error)
CreatePermission(ctx, types.CreatePermissionRequest{Key, Description, IsSystem}) (*types.Permission, error)
AddPermissionToRole(ctx, roleID, permissionID, grantedByUserID *string) error
ReplaceRolePermissions(ctx, roleID, permissionIDs []string, grantedByUserID *string) error
AssignRoleToUser(ctx, userID string, types.AssignUserRoleRequest{RoleID, ExpiresAt}, assignedByUserID *string) error
GetUserRoles(ctx, userID) ([]types.UserRoleInfo, error)
GetUserPermissions(ctx, userID) ([]types.UserPermissionInfo, error)
HasPermissions(ctx, userID, permissionKeys []string) (bool, error)
```
Permission identity is the `Key` string (e.g. `"users.read"`). Roles aggregate
permissions; users get roles. The enforce hook checks permission KEYS, not role names.

> There is also an "assign role from context" `HookAfter` hook: set
> `reqCtx.Values[models.ContextAccessControlAssignRole]` to a
> `models.AccessControlAssignRoleContext{UserID, RoleName, AssignerUserID}` and
> the plugin auto-assigns that role after the handler — handy for assigning a
> default role on sign-up.

---

## 7. Bootstrapping an admin user programmatically (ADMIN_EMAIL / ADMIN_PASSWORD)

There is **no single "create admin" helper**. Compose core services + plugin APIs.
Do this AFTER `auth.New(...)` returns (services/migrations are ready). Use
`auth.CoreServices()` and the email-password plugin's API.

Two equivalent approaches:

**A. Via email-password plugin API (creates user + account + hashes password):**
Keep a reference to the email-password plugin instance you constructed; its
exported `.Api` field (`*email_password.API`) is built during `Init`.
```go
res, err := epPlugin.Api.SignUp(ctx,
    name, os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD"),
    nil /*image*/, nil /*metadata*/, nil /*callbackURL*/, nil /*ip*/, nil /*ua*/)
// res.User.ID is the new user id; res.SessionToken set if AutoSignIn enabled.
```
Then grant the admin role:
```go
adminRole, _ := acPlugin.Api.GetRoleByName(ctx, "super_admin") // create it first if missing
_ = acPlugin.Api.AssignRoleToUser(ctx, res.User.ID,
    actypes.AssignUserRoleRequest{RoleID: adminRole.ID}, nil)
```
Make it idempotent: check `auth.CoreServices().UserService.GetByEmail(ctx, email)`
first; skip creation if it exists.

**B. Low-level via core services** (`auth.CoreServices()` →
`services.CoreServices{ UserService, AccountService, PasswordService, ... }`):
```go
cs := auth.CoreServices()
u, _ := cs.UserService.Create(ctx, name, email, true /*emailVerified*/, nil, nil)
hash, _ := cs.PasswordService.Hash(password) // PasswordService is Argon2; verify method name in services/core.go
_, _ = cs.AccountService.Create(ctx, u.ID, email, "email" /*providerID*/, &hash)
```
`UserService.Create(ctx, name, email string, emailVerified bool, image *string,
metadata map[string]any) (*models.User, error)`.
`AccountService.Create(ctx, userID, accountID, providerID string, password
*string) (*models.Account, error)` — for password login providerID is `"email"`
(see `models.AuthProviderEmail`). **UNVERIFIED:** exact `PasswordService` method
name/signature — confirm in `services/core.go` (interface is `password_service`;
core impl is `internalservices.NewArgon2PasswordService()`). Approach A avoids
this by letting the plugin hash for you — prefer A.

To seed the role itself once:
```go
desc := "Full admin"
r, _ := acPlugin.Api.CreateRole(ctx, actypes.CreateRoleRequest{Name: "super_admin", Description: &desc, IsSystem: true})
p, _ := acPlugin.Api.CreatePermission(ctx, actypes.CreatePermissionRequest{Key: "users.read", IsSystem: true})
_ = acPlugin.Api.AddPermissionToRole(ctx, r.ID, p.ID, nil)
```

---

## 8. Admin plugin — mounted routes (prefix, exposure)

**DISCREPANCY vs masterplan "tenant CRUD":** the Admin plugin does **users /
accounts / session-state / user-state / ban / impersonation** management — NOT
"tenant" CRUD. Multi-tenant ("organizations/tenants") is a SEPARATE plugin
`github.com/Authula/authula/plugins/organizations` (package `organizations`,
plugin ID `organizations`). If the masterplan means tenants → use the
organizations plugin; if it means user/ban/impersonation admin → use admin.

All admin routes are exposed via `PluginWithRoutes` (mounted by `Handler()`)
under `BasePath` (default `/auth`). Each route requires
`middleware.RequireActor(ActorUser, ActorMachine)` (impersonation start/stop
requires `ActorUser` only). **These routes do NOT auto-enforce RBAC** — guard
them yourself with a `RouteMapping` (`Plugins: ["access_control.enforce"],
Permissions: [...]`) over the `/admin/*` paths (see §6).

Verified paths (prepend BasePath, e.g. `/auth`):
- Users: `POST /admin/users`, `GET /admin/users`, `GET|PATCH|DELETE /admin/users/{user_id}`
- Accounts: `POST|GET /admin/users/{user_id}/accounts`,
  `GET|PATCH|DELETE /admin/accounts/{id}`
- User state: `GET|POST|PATCH|DELETE /admin/users/{user_id}/state`,
  `GET /admin/users/states/banned`
- **Ban:** `POST /admin/users/{user_id}/ban`, `POST /admin/users/{user_id}/unban`
- User sessions: `GET /admin/users/{user_id}/sessions`
- Session state: `GET|POST|PATCH|DELETE /admin/sessions/{session_id}/state`,
  `GET /admin/sessions/states/revoked`, `POST /admin/sessions/{session_id}/revoke`
- **Impersonation:** `GET /admin/impersonations`,
  `GET /admin/impersonations/{impersonation_id}`,
  `POST /admin/impersonations` (start, ActorUser only),
  `POST /admin/impersonations/{impersonation_id}/stop` (ActorUser only)

Programmatic admin API (`*admin.API` via plugin's `.Api` field, registered as
service `admin_service`): `CreateUser`, `GetAllUsers(cursor,limit)`,
`GetUserByID`, `UpdateUser`, `DeleteUser`, `BanUser(ctx, userID,
types.BanUserRequest, actorUserID *string)`, `UnbanUser`, `RevokeSession`,
`StartImpersonation`, `StopImpersonation`, plus account/state CRUD.

---

## 9. Email & Password plugin — routes (under BasePath)
- `POST /email-password/sign-up`
- `POST /email-password/sign-in`
- `GET  /email-password/verify-email`
- `POST /email-password/send-email-verification` (requires ActorUser)
- `POST /email-password/request-password-reset`
- `POST /email-password/change-password`
- `POST /email-password/request-email-change` (requires ActorUser)

Sign-up/sign-in JSON: `{"name","email","password","metadata","callback_url"}` /
`{"email","password","callback_url"}`. Programmatic API on `epPlugin.Api`
(`SignUp/SignIn/VerifyEmail/SendEmailVerification/RequestPasswordReset/
ChangePassword/RequestEmailChange`). Email sending requires the `email` plugin
(`github.com/Authula/authula/plugins/email`, service `mailer_service`) OR the
`Send*Email` func fields on the config — otherwise email sending is disabled
(logged warn, flows still work for token-based verification).

---

## 10. Gotchas / integration notes
- `authula.New` **panics** on any error — wrap startup accordingly; it does not
  return an error.
- A plugin is skipped unless its config `Enabled: true`.
- Set `AUTHULA_SECRET` (32-byte hex, `openssl rand -hex 32`); required in
  `GO_ENV=production`.
- Migrations (core + per-plugin) run automatically inside `New()`. To target a
  shared DB, pass `AuthConfig.DB` (a `bun.IDB`).
- The session/CSRF/access-control mechanisms run as request-lifecycle **hooks**
  ordered by stage; the access-control enforce hook is **opt-in** per route via
  metadata. Don't assume routes are protected by default — wire `RouteMapping`s.
- Keep references to the plugin instances you construct (email-password,
  access-control, admin, session) so you can reach their exported `.Api` /
  middleware after `New()`. The registry also exposes
  `auth.PluginRegistry.GetPlugin(id)` returning `models.Plugin` (type-assert).
- Shutdown: `auth.ClosePlugins()` + `auth.CloseSystems()`.

## 11. UNVERIFIED items to confirm before relying on them
- Exact `PasswordService` method name/signature (`Hash`/`Verify`) — read
  `services/core.go` lines for the `password_service` interface. Prefer the
  email-password `SignUp` API which hashes internally.
- Whether rate-limit's Redis provider reuses secondary-storage Redis or needs
  its own `Redis` config block — read `plugins/rate-limit/plugin.go` Init.
- Whether `super_admin`/`user` should map to organizations roles vs
  access-control roles in your design — both subsystems exist; access-control is
  the generic global RBAC.
</content>
</invoke>

---

## Boot smoke-test findings (2026-06-26, Authula v1.12.0 + bun v1.2.18 on MySQL 8.4)

Running the full composition root against a live MySQL surfaced three Authula
MySQL-dialect bugs and one of our own wiring mistakes. All are fixed at our
boundary (no module edits); see `internal/auth/mysqldriver.go` for the SQL shim
and `internal/http/router.go` / `internal/auth/{rbac,seed}.go` for the rest.

1. **Migrator INSERT alias (Authula bug).** `migrations.Migrator` records each
   applied migration via `ModelTableExpr("? AS schema_migration")`; bun renders
   `INSERT INTO \`auth_schema_migrations\` AS schema_migration (...)`, which is
   illegal MySQL (no table alias on INSERT) → error 1064, `New()` panics. Fixed
   by a `database/sql` driver shim that strips the bogus ` AS alias` from INSERTs
   only (SELECT/DELETE aliases are valid and kept).
2. **Bare reserved word `key` (Authula bug).** access-control's
   `access_control_permissions` MySQL DDL declares an unquoted `key VARCHAR(255)`
   column; `KEY` is reserved → 1064. Shim backticks it.
3. **`BINARY(16)` id columns (Authula bug).** access-control/admin/totp MySQL
   migrations type every id/*_id column `BINARY(16)`, but the Go models store
   36-char UUID *strings* → error 1406 "Data too long". Core tables already use
   `VARCHAR(255)`; shim rewrites `BINARY(16)` → `VARCHAR(255)` (consistent with
   the referenced `users(id)` PK).
   The shim is injected by opening Authula's pool ourselves and passing it via
   `AuthConfig.DB` (the documented hook).
4. **RBAC seed used IsSystem=true (our bug).** Authula's `AddPermissionToRole`
   returns `ErrBadRequest` when the role or permission is `IsSystem`. SeedRBAC
   now creates app-defined roles/permissions with `IsSystem=false`.
5. **Authula mount (our bug).** Authula's router self-prefixes BasePath
   (`/auth`), so its handler must see the full `/auth/...` path. We were using
   chi `Mount("/auth", …)`, which strips the prefix → 404. Now `Handle("/auth/*",
   …)` preserves it.

Result: server boots clean; migrations apply (core + access_control + admin +
totp + our app + 16 wmstore_* keystore tables); admin bootstrap creates
`admin@example.com`; `POST /auth/email-password/sign-in` with the bootstrapped
creds returns 200 with a user+session. Probes: /healthz 200, /readyz 200,
/api/v1/openapi.yaml 200, /api/v1/sessions 401 (no auth and bogus bearer),
sign-in 422 on bad creds / 200 on valid.
