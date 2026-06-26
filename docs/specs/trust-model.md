# Trust & auth model (`internal/authz` + `internal/controlbus`)

Status: implemented (R1/R2). Live-validated against better-auth 1.6.22.

How the gateway decides a request is legitimate, **with no per-request callback to the
frontend**. The gateway is a pure WhatsApp engine: it has **no human login**, no `/auth`
surface, and serves no SPA. Identity is minted by the better-auth frontend; the gateway only
*verifies* it. Masterplan §4.

## Two caller identities

There are exactly two, both resolved by one middleware (`authz.Authenticate`, two acceptors,
evaluated in order — `internal/authz/middleware.go`):

| Caller | Credential | Verified by | Resolves to |
|---|---|---|---|
| **Human** (dashboard/browser) | `Authorization: Bearer <JWT>` | JWKS signature + `iss`/`aud`/`exp` (local, cached) | `{UserID, OrganizationID(active), OrgRole, PlatformRole}` |
| **Machine** (programmatic) | `Authorization: Bearer <api-key>` or `x-api-key` | SHA-256 hash → lookup in the shared `apikey` table | `{OrganizationID, KeyID, KeyPermissions}` (no user) |

Neither acceptor matches → `401`. The resolved `authz.Principal` (`internal/authz/context.go`)
rides the request context; handlers authorize **per-resource by `organization_id`**, then gate
the action by capability (§ Authorization below).

## 1. Humans — better-auth JWT verified via JWKS

The frontend runs the better-auth **jwt** plugin: a JWKS at `GET {BETTER_AUTH_URL}/api/auth/jwks`
and short-lived (**~5 min**) **EdDSA/Ed25519** JWTs at `GET /api/auth/token`. The private key
lives in better-auth's `jwks` table (encrypted at rest); the gateway only ever sees public keys.

`internal/authz/jwt.go` (`JWTVerifier`, `github.com/lestrrat-go/jwx/v3`):

- On first use it fetches and **caches** the whole JWK set. It refreshes (a) when a token's
  `kid` is not in the cache (rate-limited by `minRefresh`, default 1m, so a flood of bad kids
  can't hammer the JWKS) and (b) lazily once the set is older than `refreshEvery` (default 1h).
- Every verify is **local**: signature against the matching `kid`, plus `iss == aud ==
  BETTER_AUTH_URL`, plus expiry. EdDSA is the default; ES256/RS256 are accepted because the key
  set advertises each key's algorithm.

**Claim shape** (set by the frontend's `definePayload`, `web/app/lib/auth/server.ts`):

| Claim | Meaning |
|---|---|
| `sub` | better-auth user id |
| `activeOrganizationId` | the active org for this session (better-auth does not auto-include it — `definePayload` adds it explicitly; an `after`-session hook seeds it) |
| `orgRole` | the member's role in the active org: `owner` / `admin` / `member` |
| `role` | platform role from the admin plugin (e.g. `super_admin`) — cross-org oversight |

So after verification the gateway has identity, the active org, **and** RBAC with zero shared
secrets and zero round-trips on the hot path. `definePayload` claims are optional on the wire —
their absence is not fatal (a user with no active org simply reaches no org-scoped resources).

**Where the browser gets the JWT:** the TanStack Start server holds the better-auth session
cookie and mints a token from `/api/auth/token`, handing it to the client. The browser then
calls the gateway directly with `Bearer`. The NDJSON stream authenticates the same way (it is
`fetch` + `ReadableStream`, not `EventSource`, so it can attach a header).

## 2. Machines — better-auth api-keys verified against the shared table

Programmatic clients present a better-auth **api-key** plugin key (prefix `wa_`). The frontend's
UI creates/lists/revokes keys; the gateway **validates locally against the shared `apikey`
table** — consistent with the hybrid-read model — so it never depends on the frontend being up.

`internal/authz/apikey.go` (`APIKeyVerifier`):

1. Hash the presented raw key with better-auth's **default** scheme and look up the row by hash.
2. Check `enabled`, `expires_at`, and that the key has an owning org.
3. Build an org-scoped `Principal` with the key's permissions and **no** `UserID`.

### LIVE-CONFIRMED `apikey` schema (better-auth 1.6.22)

The api-key path depends on replicating better-auth's deterministic hash. Confirmed against the
running version:

| Column | Meaning |
|---|---|
| `key` | the hash = **`base64url(SHA-256(rawKey))` unpadded** — *not* hex, *not* padded base64. (Go: `base64.RawURLEncoding.EncodeToString(sha256.Sum256([]byte(raw)))`, `authz.DefaultHasher`.) |
| `reference_id` | the **owning organization id**. The apiKey plugin is configured `references: "organization"`, so `reference_id` is the org and `organizationId` is required on create. The gateway resolves the owning org from this column — the key path needs no JWT. |
| `permissions` | resource→actions JSON map, e.g. `{"gateway":["read","send","manage","events"]}`. |
| `enabled` | disabled keys are rejected. |
| `expires_at`, `created_at` | `TIMESTAMP(3)`. |

`Hasher` is an interface so the scheme can be swapped if a pinned better-auth version diverges;
the **R5 contract test** mints a key in better-auth and validates it in the gateway to lock this.
**Fallback** if the hash ever proves non-replicable: `internal/authz/apikey_remote.go`
(`RemoteKeyVerifier`) calls `POST {BETTER_AUTH_URL}/api/auth/api-key/verify` behind a short-TTL
cache.

> **Pin the better-auth version.** The whole local-validation design rests on the hash and the
> column layout above; a major-version bump must re-run the contract test.

## 3. Org ownership

Resources are owned by **`organization_id`** (a better-auth organization id), never `user_id`. A
user reaches a resource through org **membership** (role owner/admin/member). Every user gets a
**personal organization** auto-created on signup, so solo use is a one-member org and "sharing a
WhatsApp connection" = inviting someone into the org. `created_by_user_id` is retained for audit.
The gateway authorizes from JWT claims (`activeOrganizationId` + `orgRole`) and the api-key's
`reference_id` — it does **not** join `member` on the hot path. (Schema: `store.md`.)

## Authorization (`internal/authz/gates.go`)

After authentication, handlers scope every query to the principal's `organization_id`, then a
capability gate authorizes the action:

- **api-keys** carry explicit permissions under the `gateway` resource:
  `{read, send, manage, events}`.
- **JWTs** map the org role: `owner`/`admin` get manage/send/read/events; `member` gets
  read/send (tunable via the org plugin's access control).
- `RequireRead` / `RequireSend` / `RequireManage` / `RequireEvents` gate route groups
  (see the router in `http-foundation.md`).
- `RequireSuperAdmin` gates cross-org oversight (`/admin/sessions`, `GET /contacts/{lid}`),
  resolved from the JWT `role`.

## Control bus, cache & instant revocation (`internal/controlbus`)

Each gateway keeps a small **positive cache** of validated keys (`internal/authz/apikey_cache.go`,
TTL ~60 s, fail-closed) so a busy client isn't a DB lookup per request. The TTL is the
**backstop**: even a missed notification stops a revoked key within the window (the `apikey` row
is gone, so the next refresh fails closed).

**Instant revocation** rides a cross-service **Redis control bus** (`PUBSUB_REDIS_URL`, defaults
to `REDIS_URL`). The frontend publishes; every gateway subscribes (`Subscriber`,
`internal/controlbus/controlbus.go`). The channels are **global literals**:

| Channel | Payload | Gateway action |
|---|---|---|
| `ctrl:apikey.revoked` | `{keyId, userId?}` | evict the cache entry for `keyId`; drop live streams it authenticated |
| `ctrl:user.banned` | `{userId}` | drop the user's cached keys + all their live streams; feed a short JWT deny-list (TTL = max JWT lifetime) |
| `ctrl:member.removed` | `{userId, organizationId}` | deny `(userId, orgId)` until JWT TTL ages out; drop that org's streams for the user |

> **Broadcast, not addressed.** The bus fans out to all gateways; the one holding the
> entry/connection acts, the rest no-op. The frontend never tracks which gateway holds a session
> for revocation purposes (session *pinning* via `gateways`/`gateway_id` is a separate concern —
> see `whatsmeow-store.md` / `store.md`).
>
> **Delivery semantics:** Redis pub/sub is fire-and-forget — a gateway that's down when a message
> is published misses it, which the 60 s TTL backstop + boot reconcile cover. Promote `ctrl:*` to
> a Redis Stream with consumer groups if at-least-once is later required; call sites keep shape.

## Boot reconciliation & orphan-guard

The in-memory cache is cold after a restart, so no stale *cached* key survives a reboot. The
catch-up is over **persistent** authorizations, a one-time startup sweep:

1. Before the Session Manager (`internal/wa/manager.go`) resumes each WhatsApp session from the
   SQLite keystore, the gateway checks the session's **owning org still exists and is enabled** in
   MySQL and **skips + marks `STOPPED`** any whose org was deleted/disabled while it was down
   (**orphan-guard**, see `store.go`/`organization.go`).
2. It reconciles persisted deny-list / known-key state against the shared `apikey` table, dropping
   entries whose key is now revoked/expired/disabled.

This closes the window for `ctrl:*` messages missed during downtime, complementing the live
subscriber and the 60 s cache TTL.

## JWT lifecycle (refresh & revocation)

better-auth has **no separate refresh token** — the **session is the long-lived, revocable
credential**, the JWT is a short access token minted from it.

- **Refresh:** the browser (holding the session cookie on the frontend's domain) re-calls
  `/api/auth/token`. `expirationTime` ~5 min keeps the revocation window tiny.
- **Revoke (blocks refresh):** revoke the **session** (better-auth admin endpoints, or logout).
  Once gone, `/api/auth/token` stops minting; access ends within ≤ the JWT TTL.
- **Instant kill (in-flight JWTs):** publish `ctrl:user.banned` (§ above); gateways add the user
  to the short JWT deny-list and drop live streams.
- **Streams vs short JWTs:** an NDJSON stream is authenticated **at connect**; the client
  refreshes its JWT and reconnects (`since={lastEventId}`, §11) — a 5-min TTL triggers a
  transparent reconnect, never tears the consumer's view.

## Files

| File | Responsibility |
|---|---|
| `internal/authz/jwt.go` | `JWTVerifier`: JWKS fetch+cache, JWT verify, claim extraction |
| `internal/authz/apikey.go` | `APIKeyVerifier`, `Hasher` (`DefaultHasher` = SHA-256→base64url), `KeyVerifier` |
| `internal/authz/apikey_remote.go` | `RemoteKeyVerifier` fallback (`/api/auth/api-key/verify` + cache) |
| `internal/authz/apikey_cache.go` | positive key cache (TTL backstop, evict by keyId/userId) |
| `internal/authz/middleware.go` | `Authenticate` — two-acceptor middleware |
| `internal/authz/gates.go` | `RequireRead/Send/Manage/Events/SuperAdmin` capability gates |
| `internal/authz/context.go` | `Principal` + context accessors |
| `internal/authz/cors.go` | CORS for `FRONTEND_ORIGINS` (browser → gateway) |
| `internal/controlbus/controlbus.go` | `ctrl:*` subscriber → cache evict + stream drop |

## How it's tested

Table-driven Go tests with the JWKS fetcher, key repo, cache and stream-dropper faked behind the
consumer interfaces (`jwt_test.go`, `apikey_test.go`, `apikey_cache_test.go`, `gates_test.go`,
`middleware_test.go`, `cors_test.go`, `controlbus_test.go`). The **trust seam** is locked by the
R5 contract tests: one mints a better-auth JWT and verifies it in the gateway; one creates a
better-auth api-key and validates it in the gateway. Both are CI gates.

Run: `CGO_ENABLED=0 go test ./internal/authz/... ./internal/controlbus/...`.
