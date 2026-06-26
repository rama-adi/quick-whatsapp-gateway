# Auth & Tenancy (`internal/auth`)

> ⚠️ **SUPERSEDED (v1).** This describes the removed **Authula** auth. v2 replaces it with
> better-auth on the frontend (JWKS-verified JWTs + api-keys + org ownership). See
> [`../../masterplan-mvp.md`](../../masterplan-mvp.md) §4 and [`_V2-STATUS.md`](_V2-STATUS.md).
> To be rewritten as `trust-model.md` in **R1**. Do not follow this for v2.

Status: implemented (M1).

Wires [Authula](https://github.com/Authula/authula) `v1.12.0` as an embedded Go
library: plugin assembly, RBAC seeding, super-admin bootstrap, the
`USER_PANEL_ENABLED` gate, request accessors/middleware, and the app-side
`tenants` mirror. Source-of-truth recon: `docs/specs/_recon-authula.md`.

## Scope

- **`Build(cfg)`** — construct `*authula.Auth` with all §10 plugins enabled.
- **`SeedRBAC(ctx, store)`** — idempotently seed roles + permission keys.
- **`BootstrapAdmin(ctx, ...)`** — idempotently provision the super-admin.
- **`TenantSyncer.SyncTenant(ctx, userID, email)`** — upsert the `tenants` row.
- Request accessors: `CookieAuthMiddleware`, `OptionalCookieAuth`,
  `ActorFromRequest`, `CurrentTenantID`, `RequireRole`, `RequirePermission`.

Out of scope here: API keys (custom, see `api-keys.md` — Authula has no key
plugin), the HTTP router (Phase 3 mounts `Auth.Handler()` under `/auth`).

## Files

| File | Responsibility | DB-free / tested |
|---|---|---|
| `config.go` | ENV-shaped `Config`, normalization, sign-up gate | yes |
| `rbac.go` | the role names + permission KEY SET + seed plan | yes |
| `ports.go` | consumer-defined interfaces (see below) | yes |
| `plugins.go` | pure plugin-assembly description + RouteMappings | yes |
| `seed.go` | `SeedRBAC` | yes (fakes) |
| `bootstrap.go` | `BootstrapAdmin` | yes (fakes) |
| `tenant.go` | `TenantSyncer.SyncTenant` | yes (fakes) |
| `auth.go` | `Build` + request accessors + Require* guards | guards tested; `Build` skipped (needs live DB) |
| `adapters.go` | map live Authula APIs → ports | exercised only via `Build` |
| `http.go` | §11 error envelope for guard denials | yes (via guards) |

## Key types / interfaces

`internal/auth` follows the consumer-defined-interface convention: it depends on
small interfaces it declares, and `Build` adapts the real Authula instances into
them (`adapters.go`). This keeps all logic unit-testable with fakes and honors
the parallel-safe import rule (no sibling `internal/*` imports; `TenantStore` is
declared here, not imported from `internal/store`).

- **`RBACStore`** — the slice of access-control we drive: `GetAllRoles`,
  `CreateRole`, `GetAllPermissions`, `CreatePermission`, `GetRolePermissionKeys`,
  `AddPermissionToRole`, `GetUserRoleNames`, `AssignRoleToUser`,
  `UserHasPermission`.
- **`UserDirectory`** — `GetByEmail` (returns `(nil,nil)` when absent).
- **`SignUpStore`** — `SignUp(name,email,password) -> userID`.
- **`TenantStore`** — `UpsertTenant(id,email,now)` (Phase 3 wires the MySQL repo).
- **`Clock`** — injectable `NowMs()` for deterministic tenant timestamps.

### Plugins enabled (`§10`)

secondary-storage (Redis), rate-limit (Redis provider), session, csrf,
email-password, totp, access-control, admin — each with `Enabled: true` (Authula
silently skips a plugin whose config `Enabled` is false).

### RBAC seed (the contract)

Roles: `super_admin`, `user` (both `IsSystem`). Permission keys:
`users.read`, `users.write`, `sessions.manage`, `admin.access`. `super_admin`
holds **all** keys; `user` holds **none** (tenant isolation is enforced by the
app via `tenant_id`, not by an Authula permission). The enforce hook checks
permission **keys**, so admin routes are gated by `admin.access` via a
`RouteMapping`, not by role name.

## Decisions

- **`Build` panics-by-proxy.** `authula.New` panics on any init error (it does
  not return an error); there is no partial-success state, so `Build` lets it
  panic and the composition root treats startup as fatal.
- **MySQL provider, not a shared pool.** `Build` passes
  `DatabaseConfig{Provider:"mysql", URL: MySQLDSN}` and lets Authula open + own
  its pool and run its migrations. Sharing an injected `*bun.IDB` is supported by
  Authula (`AuthConfig.DB`) but adds coupling for no M1 benefit; revisit if pool
  pressure shows up.
- **`USER_PANEL_ENABLED` → `EmailPasswordPluginConfig.DisableSignUp`.** When the
  user panel is off, self-registration is disabled at the plugin (`DisableSignUp:
  true`) so only the bootstrapped admin can authenticate; **login stays enabled**.
  `Config.signUpEnabled()` is the single source of truth and is unit-tested.
- **Idempotency via reconcile, not sentinels.** Authula's `GetRoleByName`
  surfaces a not-found as an `internal/errors` sentinel we cannot import, but the
  `GetAll*` reads never error on absence. `SeedRBAC`/`BootstrapAdmin` reconcile
  against current state (create-if-missing, attach-if-not-attached,
  assign-if-not-held), so re-running is a clean no-op.
- **Admin gate via `RouteMapping`, paths un-prefixed.** Authula prepends
  `BasePath` to mapping paths itself (`ConvertRouteMetadata` +
  `ApplyBasePathToMetadataKey`), so we pass `/admin/*`, which resolves to
  `<BasePath>/admin/*`. Double-prefixing was a real foot-gun and is covered by a
  test.
- **`Require*` guards are for our own routes.** Authula-owned routes are gated by
  the enforce hook + RouteMappings. `RequireRole`/`RequirePermission` exist for
  chi routes mounted outside Authula's router; they 401 unauthenticated, 403 on
  missing role/permission, and emit the §11 error envelope. A `resolveTenantID`
  field (defaults to `CurrentTenantID`) makes them testable without a live DB.

## Discrepancies vs masterplan (from recon)

1. **RBAC is generic roles+permissions, not a fixed `super_admin`/`user` enum.**
   We create those roles ourselves in `SeedRBAC` (recon §6). They are app
   conventions, not Authula constants.
2. **The Admin plugin manages users/ban/impersonation, NOT "tenants".** The
   masterplan's "tenant CRUD via Admin plugin" maps onto Authula's user-admin
   surface; true multi-member tenants would need the separate `organizations`
   plugin (deferred, §17). Our `tenants` table is a thin mirror keyed by the
   Authula user id.
3. **No "create admin" helper.** `BootstrapAdmin` composes the email-password
   `SignUp` API + access-control `AssignRoleToUser` (recon §7).
4. **`REDIS_URL` env wins** over the configured secondary-storage URL, and Redis
   failure falls back to in-memory (recon §4) — operationally important.

## How it's tested

Table-driven Go tests, all external boundaries faked via the consumer interfaces;
no test calls `authula.New` (it needs a live MySQL — the live path is integration,
not unit). Coverage of the real logic:

- `plugins_test.go` — every plugin enabled; `USER_PANEL_ENABLED` → `DisableSignUp`
  gating; Redis backing; admin `RouteMapping` shape (un-prefixed path, enforce
  plugin, `admin.access`).
- `seed_test.go` — the permission/role KEY SET is locked; clean seed creates
  everything once; re-seed is a no-op; errors propagate.
- `bootstrap_test.go` — new vs existing user; idempotent re-run; no-op when
  unconfigured; clear error if `SeedRBAC` was skipped; lookup errors surface.
- `tenant_test.go` — `SyncTenant` forwards id/email/now, trims input, guards
  empty id, propagates store errors, defaults the clock.
- `guard_test.go` — `RequireRole`/`RequirePermission` 401/403/200 matrix +
  §11 error-envelope shape.

Run: `CGO_ENABLED=0 go test ./internal/auth/...`.
