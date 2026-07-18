# Trust & auth model (`internal/authz` + `internal/controlbus` + `internal/assertion`)

> **Target migration, not current runtime (gRPC control-plane Increment 0).** Public JWT/API-key
> authentication and all end-user authorization remain at the API front door. Gateways will receive
> only authorized service commands and authenticate the API with per-gateway mTLS; they never receive
> user credentials or permission claims. Development uses a persisted local root plus online
> intermediate (the root signs only that intermediate). Production is designed behind a
> `CertificateSigner` seam, with Vault PKI as the reference implementation rather than a locked
> vendor. The current Ed25519 router assertion described below remains active until gRPC slices cut
> over.

> **Increment 2.0/2.1a foundation (unwired):** normalized enrollment-token, authority,
> gateway-certificate, and audit-event records now exist. Enrollment persistence contains only a
> SHA-256 digest and safe prefix; authority private keys are ciphertext plus nonce and key id. No
> token issuance, CSR validation, signer, mTLS listener, API, or UI consumes these tables yet.
> Pure 2.1a policy uses canonical versioned 256-bit bearer tokens, strict token-bound Ed25519
> SPIFFE CSRs, 24-hour issuer-capped client+server-auth leaves, and AES-256-GCM CA-key envelopes
> whose AAD binds authority id, kind, certificate fingerprint, and encryption-key id.

Status: implemented (R1/R2). Live-validated against better-auth 1.6.22.

> **Central-router (Increment A) — read this first.** Authentication now **terminates at the
> router**, not the gateway. The two-acceptor authn (`internal/authz.Authenticate` + the JWT /
> api-key verifiers + the positive cache) and the `ctrl:*` control-bus subscriber run **only on the
> router** ([`router.md`](router.md)). The gateway no longer verifies end-user JWTs or api-keys and
> no longer wires `internal/controlbus`; it **trusts the router's request-bound Ed25519 internal
> assertion** (`internal/assertion`) and rebuilds the principal from it (see "Router assertion"
> below). The **authz split is unchanged in spirit** — *verify* at the router, *gate + scope* at the
> gateway. The sections below describe the verifiers/cache/control-bus that the **router** now runs;
> the gateway keeps only `gates.go` + `context.go` + the assertion-verify middleware.

How a request is decided legitimate, **with no per-request callback to the frontend**. The gateway
is a pure WhatsApp engine: it has **no human login**, no `/auth` surface, and serves no SPA. Identity
is minted by the better-auth frontend; the **router** *verifies* it (the gateway only verifies the
router). Masterplan §4.

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
lives in better-auth's `jwks` table (encrypted at rest); the router fetches only public keys.

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

So after verification the router has identity, the active org, **and** RBAC with zero shared
secrets and zero round-trips on the hot path. `definePayload` claims are optional on the wire —
their absence is not fatal (a user with no active org simply reaches no org-scoped resources).

**Where the browser gets the JWT:** the TanStack Start server holds the better-auth session
cookie and mints a token from `/api/auth/token`, handing it to the client. The browser then
calls the **router** with `Bearer` for REST or ticket minting. The ticketed WebSocket is implemented;
gateway NDJSON is removed ([`router.md`](router.md)).

## 2. Machines — better-auth api-keys verified against the shared table

Programmatic clients present a better-auth **api-key** plugin key (prefix `wa_`). The frontend's
UI creates/lists/revokes keys; the router **validates locally against the shared `apikey`
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
| `reference_id` | the **owning organization id**. The apiKey plugin is configured `references: "organization"`, so `reference_id` is the org and `organizationId` is required on create. The router resolves the owning org from this column — the key path needs no JWT. |
| `permissions` | resource→actions JSON map, e.g. `{"gateway":["read","send","manage","events"]}`. |
| `enabled` | disabled keys are rejected. |
| `expires_at`, `created_at` | `TIMESTAMP(3)`. |

`Hasher` is an interface so the scheme can be swapped if a pinned better-auth version diverges;
the **R5 verifier contract test** mints a key in better-auth and validates the Go verifier now
consumed by the router (historically it was gateway-wired) to lock this.
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
The router authorizes from JWT claims (`activeOrganizationId` + `orgRole`) and the API key's
`reference_id`; the gateway receives the private assertion. (Schema: `store.md`.)

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

> **Authz split (central-router, Increment A).** *Verify* runs at the **router**; *gate + scope*
> runs at the **gateway**. The router authenticates the caller, resolves the `Principal`, and
> enforces **org isolation** on session-scoped routes (session's `organization_id` must equal the
> caller's org, else `404`; `super_admin` bypasses). The gateway then re-applies the **capability
> gates** above (`RequireRead/Send/Manage/Events/SuperAdmin`, `gates.go`) and its **org-scoped store
> queries** (`WHERE organization_id = ?`), reading the principal from the verified router assertion —
> defense in depth at the data layer. These gates and queries are **unchanged**; only the source of
> the principal changed (assertion, not direct JWT/api-key verify).

## Router assertion (`internal/assertion`)

The router → gateway trust seam. The router strips the caller's `Authorization`/`X-Api-Key` and
attaches a **request-bound, single-use Ed25519 JWS** in the `X-Internal-Assertion` header; the
gateway's `assertion.Middleware` (on `/api/v1`) verifies it and rebuilds the `Principal`. The router
holds the Ed25519 **private** key (`Minter`); the gateway holds only the **public** key (`Verifier`,
fetched from `ROUTER_JWKS_URL` via a cached `RemoteKeySet`), so a compromised gateway cannot forge an
assertion. Full claim set + custody live in [`router.md`](router.md).

**Gateway verification order:** signature (router JWKS) → `aud` == own `GATEWAY_ID` → `iss` ==
`ROUTER_ASSERTION_ISSUER` (default `"router"`) → `exp`/`iat` within ~5s skew → `method`/`path`/
`bodyHash` match the actual request → coherent principal kind and required identity fields →
`jti` not seen before (in-memory `NonceCache` anti-replay).

> **Note.** The gateway uses a **dedicated jwx-based verifier in `internal/assertion`** (not the
> plain `JWTVerifier`) because it must extract the request-binding claims (`method`/`path`/
> `bodyHash`/`session`/`jti`/principal) the plain verifier doesn't surface — reusing the same
> JWKS-cache pattern repointed at `ROUTER_JWKS_URL`.

## Control bus, cache & instant revocation (`internal/controlbus` — now the router's)

> **Central-router (Increment A):** the `ctrl:*` subscriber + the api-key positive cache moved to
> the **router**; the **gateway no longer wires `internal/controlbus`** or keeps a key cache (it
> authenticates nothing). On `ctrl:apikey.revoked` the router evicts its positive cache. The live
> **stream-drop** on `ctrl:user.banned`/`ctrl:member.removed` is implemented for router WebSockets.
> The description below is the behavior, now owned by the router.

The router keeps a small **positive cache** of validated keys (`internal/authz/apikey_cache.go`,
TTL ~60 s, fail-closed) so a busy client isn't a DB lookup per request. The TTL is the
**backstop**: even a missed notification stops a revoked key within the window (the `apikey` row
is gone, so the next refresh fails closed).

Cache eviction also advances a process-wide authorization generation, including when the key was
not cached. A database verification that started before that increment may not publish its result;
it re-reads the authoritative row after the eviction. This creates a concrete happens-before edge
between control-bus handling and positive-cache insertion, preventing an in-flight lookup from
resurrecting a key immediately after revocation. Cache locks are not held during database I/O, so
unrelated hits and evictions remain responsive.

**Instant revocation** rides a cross-service **Redis control bus** (`PUBSUB_REDIS_URL`, defaults
to `REDIS_URL`). The frontend publishes; the router subscribes (`Subscriber`,
`internal/controlbus/controlbus.go`). The channels are **global literals**:

| Channel | Payload | Router action |
|---|---|---|
| `ctrl:apikey.revoked` | `{keyId, userId?}` | evict the cache entry for `keyId`; drop live streams it authenticated |
| `ctrl:user.banned` | `{userId}` | drop the user's cached keys + all their live streams; feed a short JWT deny-list (TTL = max JWT lifetime) |
| `ctrl:member.removed` | `{userId, organizationId}` | deny `(userId, orgId)` until JWT TTL ages out; drop that org's streams for the user |

> **Broadcast, not addressed.** The global channel remains independent of stack prefixes; the
> router holding the entry/connection acts. The frontend never tracks which gateway holds a session
> for revocation purposes (session *pinning* via `gateways`/`gateway_id` is a separate concern —
> see `whatsmeow-store.md` / `store.md`).
>
> **Delivery semantics:** Redis pub/sub is fire-and-forget — a router that's down when a message
> is published misses it; the 60 s cache TTL and authoritative DB lookup after cold start bound the
> revocation window. Promote `ctrl:*` to
> a Redis Stream with consumer groups if at-least-once is later required; call sites keep shape.

## Boot reconciliation & orphan-guard

Two separate restart safeguards apply; there is no persisted deny-list/known-key reconciliation:

- Before the Session Manager (`internal/wa/manager.go`) resumes each WhatsApp session from the
   SQLite keystore, the gateway checks the session's **owning org still exists and is enabled** in
   MySQL and **skips + marks `STOPPED`** any whose org was deleted/disabled while it was down
   (**orphan-guard**, see `store.go`/`organization.go`).
- After a router restart its in-memory positive cache is empty. The next credential use reads the
  authoritative `apikey` row; the 60-second TTL bounds a live router's missed-pub/sub window.

The gateway orphan guard protects WhatsApp session ownership. Router DB lookup plus cache TTL
protect public credential revocation; they are independent mechanisms.

## JWT lifecycle (refresh & revocation)

better-auth has **no separate refresh token** — the **session is the long-lived, revocable
credential**, the JWT is a short access token minted from it.

- **Refresh:** the browser (holding the session cookie on the frontend's domain) re-calls
  `/api/auth/token`. `expirationTime` ~5 min keeps the revocation window tiny.
- **Revoke (blocks refresh):** revoke the **session** (better-auth admin endpoints, or logout).
  Once gone, `/api/auth/token` stops minting; access ends within ≤ the JWT TTL.
- **Instant kill (in-flight JWTs):** publish `ctrl:user.banned` (§ above); the router rejects the
  principal and drops affected WebSockets.
- **Realtime vs short JWTs:** JWT/API-key auth occurs at ticket mint; reconnect mints a new
  single-use ticket carrying the `since` cursor.

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
| `internal/authz/cors.go` | CORS for `FRONTEND_ORIGINS` (browser → **router**; the gateway no longer mounts CORS) |
| `internal/controlbus/controlbus.go` | `ctrl:*` subscriber → cache evict (+ stream drop in Increment B) — **consumed by the router now**, not the gateway |
| `internal/assertion` | router → gateway Ed25519 internal assertion: `Minter` (router, private key), `Verifier` + `NonceCache` (gateway, public key from `ROUTER_JWKS_URL`) |

## How it's tested

Table-driven Go tests with the JWKS fetcher, key repo, cache and stream-dropper faked behind the
consumer interfaces (`jwt_test.go`, `apikey_test.go`, `apikey_cache_test.go`, `gates_test.go`,
`middleware_test.go`, `cors_test.go`, `controlbus_test.go`). The **trust seam** is locked by the
R5 contract tests: one mints a better-auth JWT and one creates a better-auth API key, then each is
validated by the Go verifier now consumed by the router (historically gateway-wired). Both are CI
gates.

Run: `CGO_ENABLED=0 go test ./internal/authz/... ./internal/controlbus/...`.
