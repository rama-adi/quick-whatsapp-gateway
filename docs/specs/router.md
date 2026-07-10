# Central router (`cmd/router` + `internal/router` + `internal/assertion`)

Status: implemented (Increment A + Increment B of
[`../plans/plan-router-impl.md`](../plans/plan-router-impl.md)). Live: the REST broker, auth
termination, gateway registry lifecycle (Layer 1), and the realtime **WebSocket** endpoint
(`POST /api/v1/realtime/ticket` + `GET /api/v1/realtime`, single-use Redis tickets, Redis `evt:*`
fan-out, control-bus stream-drop). Realtime is **WebSocket-only**: the frontend WS client redeems
a ticket against the router, and the gateway's legacy NDJSON `/events` transport has been removed
(the gateway only publishes events to Redis now). See "Realtime" below.

## Purpose

The router is the system's **single front door** and **single trust boundary** in front of the
gateways. Callers use *one* base URL + their token + a session id; the router brokers the call to
whichever gateway owns that session. This solves three problems the
direct-to-gateway design had once there is (or might be) more than one gateway: host discovery is no
longer the caller's problem, the version-pinned trust seam (better-auth api-key hash + JWKS verify)
lives in exactly **one** place instead of N copies, and revocation/realtime have a single owner.

The router is **stateless** (no per-session state of its own — it reads the shared MySQL registry +
`wa_sessions`, and uses Redis for the control bus). The gateway becomes a lean internal WhatsApp
engine that trusts the router (see [`trust-model.md`](trust-model.md), [`http-foundation.md`](http-foundation.md)).

## Request lifecycle

Every `/api/v1` request flows through four steps:

1. **Authenticate** the end-user caller. The two-acceptor authn (`internal/authz.Authenticate`
   with `JWTVerifier` + `APIKeyVerifier`, the latter behind `CachingKeyVerifier`) runs **only on the
   router now** — a `Bearer` JWT verified against the better-auth JWKS, or a better-auth api-key
   verified against the shared `apikey` table. Neither → `401`. This resolves the full
   `authz.Principal` (org, principal kind, userId, orgRole, platform role, keyId, permissions).
2. **Resolve session → owning gateway + enforce org isolation.** For a request naming a specific
   session, the router looks up the session (`SessionRepo`) and its owning gateway in the registry
   (`GatewayRepo`). **Org isolation (load-bearing):** the session's `organization_id` must equal the
   caller's org, else `404` (a `super_admin` platform role bypasses this for cross-org oversight).
3. **Mint the internal assertion.** The router strips the inbound `Authorization` / `X-Api-Key` and
   attaches a request-bound Ed25519 internal assertion in the `X-Internal-Assertion` header (see
   "Internal assertion" below).
4. **Reverse-proxy** the request to the owning gateway's `base_url`
   (`net/http/httputil.ReverseProxy`). The gateway verifies the assertion, rebuilds the principal
   from it, and re-applies its capability gates + org-scoped queries.

## Routing rules

| Request | Target gateway | How chosen |
|---|---|---|
| `POST /api/v1/sessions` (create) | **placement** | least-loaded `active` gateway via `GatewayRepo.PickForPlacement` |
| any path naming a specific session (`.../sessions/{id}...`, incl. `/admin/sessions/{id}:action`) | the session's **owning** gateway | `wa_sessions.gateway_id` (authoritative) → registry `base_url` |
| everything else — webhooks, `GET /sessions` list, admin list | **any active** gateway (gateway-agnostic) | served from shared MySQL, so any `active` gateway answers |

**Stranded session.** If the owning gateway is missing, not `active`, or its heartbeat is missing,
stale, or implausibly far in the future, the router returns **`503 gateway_unavailable`** with a
clear message rather than a silent hang (the `gateway_unavailable` domain error code → HTTP 503).

## Internal assertion (`internal/assertion`)

The router → gateway trust seam (plan D3). A **request-bound, single-use Ed25519 JWS** minted per
proxied request (compact JWS via `lestrrat-go/jwx`). The router holds the Ed25519 **private** key
(the `Minter`); the gateway holds only the **public** key (the `Verifier`, fetched from
`ROUTER_JWKS_URL` via a cached `RemoteKeySet`). A compromised gateway therefore **cannot forge**
assertions.

**Claims (all load-bearing):**

| Claim | Purpose |
|---|---|
| `iss` | router identity (the `ROUTER_ISSUER`, default `"router"`) |
| `aud` | the target `gateway_id` — cannot be replayed against another gateway |
| `iat` / `exp` (~30s) | tight freshness window |
| `jti` | one-time nonce (anti-replay) |
| `org`, principal kind (`user` \| `apikey`), `userId`, `orgRole`, platform `role`, `keyId`, `permissions` | the resolved principal the gateway gates on |
| `session` | the specific session being acted on |
| `method`, `path` (`path[?query]`) | cannot be replayed against a different endpoint |
| `bodyHash` = `base64url(SHA-256(body))` | cannot be reused with a swapped payload |

**Gateway verification order:** signature (against the router's JWKS) → `aud` == own `GATEWAY_ID`
→ `iss` == `ROUTER_ASSERTION_ISSUER` → `exp`/`iat` within ~5s skew → `method`/`path`/`bodyHash`
match the actual request → coherent principal kind and required identity fields → `jti` not seen
before (in-memory `NonceCache` anti-replay). Requires router/gateway clocks on NTP with small skew
tolerance.

> **Deviation from D3 (recorded).** D3's plain intent was to reuse the existing `JWTVerifier`
> repointed at `ROUTER_JWKS_URL`. In practice the gateway uses a **dedicated jwx-based verifier in
> `internal/assertion`** because it must extract the extra request-binding claims (`method`/`path`/
> `bodyHash`/`session`/`jti`/principal) that the plain `JWTVerifier` does not surface. It **reuses
> the same JWKS-cache pattern** repointed at `ROUTER_JWKS_URL`, honoring D3's modular intent (same
> jwx machinery, different URL) while correctly handling the additional claims.

The router publishes its assertion-verification JWKS at **`GET /.well-known/router-jwks.json`**, so
the gateway picks up key rotation automatically (no static key to distribute). The default issuer
constant is `"router"` (`config.DefaultRouterIssuer`); minter and verifier agree on it with no extra
config.

## Control-bus ownership

The control-bus (`ctrl:*`) subscriber now lives on the **router** (moved off the gateways — see
[`queue.md`](queue.md), [`trust-model.md`](trust-model.md)). On `ctrl:apikey.revoked` the router
**evicts the api-key positive cache** so a revoked key stops working within the window. (Live
WebSocket stream-drop on revocation — `ctrl:user.banned` / `ctrl:member.removed` dropping live
connections — arrives with the realtime endpoint in **Increment B**.)

## Other surfaces

- Serves the **public OpenAPI spec** at `GET /api/v1/openapi.yaml` (the gateway no longer serves it
  — plan D9). The router owns the public route surface + CORS (`FRONTEND_ORIGINS`); browsers hit the
  router, not the gateway.
- Health probes: `GET /healthz`, `GET /readyz`.
- `internal/dbconn` — the shared MySQL connection helper the router (and gateway) use to reach the
  shared app-data DB / registry.

## Config / env (`config.LoadRouter` → `config.RouterConfig`)

| Var | Default | Purpose |
|---|---|---|
| `ROUTER_HTTP_ADDR` | `:8090` | listen addr |
| `ROUTER_PUBLIC_URL` | — | external base URL of the router |
| `ROUTER_ISSUER` | `router` | the assertion `iss` the minter stamps (= `config.DefaultRouterIssuer`) |
| `ROUTER_ED25519_PRIVATE_KEY` | — (**required**) | base64 seed or full Ed25519 private key the minter signs with |
| `MYSQL_DSN` | — | shared app-data DSN (registry + `wa_sessions` + `apikey` read) |
| `REDIS_URL` | — | work Redis |
| `PUBSUB_REDIS_URL` | `${REDIS_URL}` | control-bus `ctrl:*` pub/sub |
| `BETTER_AUTH_URL` | — (**required**) | the JWT `iss`/`aud` to enforce |
| `BETTER_AUTH_JWKS_URL` | `${BETTER_AUTH_URL}/api/auth/jwks` | JWKS to fetch/cache (derived from `BETTER_AUTH_URL` if unset) |
| `FRONTEND_ORIGINS` | — | comma-list of allowed CORS origins (browser → router) |
| `REDIS_PREFIX` | — | key/channel namespace |
| `LOG_LEVEL` | — | structured logging |

On the **gateway** side the matching config is `ROUTER_JWKS_URL` (required at runtime — the router's
JWKS for assertion verify) + `ROUTER_ASSERTION_ISSUER` (default `router`); `GATEWAY_ID` is the
assertion audience. See [`http-foundation.md`](http-foundation.md).

## Realtime (Increment B — implemented)

The router is the single client-facing realtime endpoint. A browser cannot set an `Authorization`
header on a WebSocket, so authz happens once at **ticket mint** and the WS handshake merely redeems a
short-lived, single-use ticket (`internal/router/realtime.go`, `internal/stream` `Pump`):

- `POST /api/v1/realtime/ticket` — bearer-authenticated, scope-bearing, **authz-at-mint**: events
  capability for `session`/`organization` scopes, `super_admin` for the admin `firehose`. The
  *resolved* scope (org/session/events/since/principal) is stored in Redis `{prefix}:rt:ticket:{id}`
  with a ~30s TTL. Returns `{ticket, expiresInSeconds, url}`.
- `GET /api/v1/realtime?ticket=…` — WS upgrade that **atomically `GETDEL`s** the ticket (single-use
  even across replicas → a second connect gets nothing), subscribes per scope
  (`evt:{org}:{session}` / `evt:{org}:*` / `evt:*`), applies the event-type filter, registers the
  live connection so `ctrl:*` can drop it on revocation, and replays from `event_log` via the
  ticket's `since` for org/session scopes (firehose is live-tail only).

Events reach the router over the **shared Redis `evt:*` fan-out** the gateways already publish to
(the gateway keeps its `stream.Publisher`). The transport-agnostic `stream.Pump` runs the
subscribe → connected → replay → tail loop and writes WebSocket text frames via a `Sink`.

The frontend WS client (`web/app/lib/events/stream.ts`) mints a ticket then opens the WebSocket;
the gateway's legacy NDJSON `/events` transport has been **removed** — nothing references it. The
plan's "direct-push" event seam (gateway → router ingest, no Redis in the event path) is an
optimization deferred in favor of reusing the existing Redis fan-out.

> **Increment 0 (also not done):** the code-first OpenAPI foundation (huma; shared Go types →
> generated `docs/openapi.yaml`). For now `docs/openapi.yaml` remains the hand-authored contract the
> router serves.

## Future: direct-push events / Layer 2 keystore portability

- **Direct-push event seam (Increment B).** The gateway → router event ingest (authenticated by the
  same Ed25519 mechanism, gateway → router direction) is the default transport once realtime lands;
  shared-Redis `evt:*` is the HA variant behind the existing `EventSink` interface. Neither is wired
  yet.
- **Layer 2 — keystore portability (deferred).** The whatsmeow keystore is gateway-local SQLite, so
  a session's crypto material lives on exactly one box; this is what makes *removing* a gateway
  non-trivial. True drain/rebalance/failover needs a **shared keystore** (whatsmeow `sqlstore` on
  Postgres — separate infra). Layer 1 (this increment) makes accounting clean *within* the
  local-keystore constraint; portability is a later implementation swap behind the keystore consumer
  interface. See [`whatsmeow-store.md`](whatsmeow-store.md).
