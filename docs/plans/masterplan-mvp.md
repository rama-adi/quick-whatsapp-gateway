# whatsmeow Gateway — Implementation Spec (v2)

A self-hostable WhatsApp platform with three independently deployable runtime roles:

- **Router** — the public REST/WebSocket front door and trust boundary. It owns JWT/API-key
  verification, CORS, generated Huma OpenAPI, session routing, and realtime fan-out.

- **Gateway** — a focused **Go** service that talks WhatsApp over **whatsmeow** and nothing
  else: pairing, inbound/outbound pipelines, and webhooks. It has no public human login or public
  credential verification; during migration it trusts the router's private assertion.
- **Frontend** — a fullstack **TanStack Start** app that owns **all human identity and the
  admin/user surfaces** via **better-auth** (on MySQL), plus the dashboard, viewer, and
  contacts UI. It can be hosted separately from the gateway.

The frontend issues identity; the router verifies better-auth JWTs/API keys and currently brokers
private HTTP calls to gateways. MySQL and Redis remain shared runtime dependencies until the gRPC
control-plane migration removes them from gateways.

> **gRPC control-plane migration (Increment 0; target locked, runtime not cut over).** The target
> architecture makes the Go API/control plane the only public front door and turns gateways into
> private WhatsApp engines reached over versioned gRPC/mTLS, with no gateway MySQL or Redis runtime
> dependency. The current router, HTTP reverse proxy, Ed25519 assertion, shared-database gateway,
> and Redis paths described below remain the implemented runtime until their replacement increments
> land. The migration design of record is
> [`plan-grpc-control-plane.md`](./plan-grpc-control-plane.md).

> **Pre-release migration policy.** There is no production compatibility burden. The control-plane
> migration may reshape packages, schemas, APIs, and deployment topology to reach the clean target
> without compatibility shims, provided each increment remains buildable, testable, and deployable.

> **Legal / risk notice (ship in README + dashboard footer):** This uses an unofficial
> WhatsApp client. WhatsApp prohibits bots/unofficial clients; automated use may violate its
> Terms and get numbers **banned**. Built-in rate limiting and human-mimicry reduce but don't
> remove the risk. Use at your own risk.

> **History:** This supersedes the v1 single-binary design (Go + embedded Authula + embedded
> React Router SPA). The v1 code, spec, and progress tracker are preserved at git tag `mvp-v1`
> — check it out to read them.

---

## 0. Contents

1. Goals & non-goals · 2. Architecture · 3. Components & responsibilities · 4. Trust & auth
model (JWKS + API keys) · 5. Session model · 6. Storage planes & data ownership · 7. Data
model (DDL) · 8. WhatsApp lifecycle, pairing & admin number · 9. Inbound pipeline · 10.
Outbound pipeline · 11. Eventing & event schema · 12. Frontend (TanStack Start + better-auth)
· 13. Public REST API (router) · 14. Configuration · 15. Packaging · 16. Repo layout · 17. Migration
plan (from v1) · 18. Deferred · 19. Open micro-decisions · 20. Engineering conventions · 21.
Local development

---

## 1. Goals & non-goals

**Goals (v2)**

- **Clean split of concerns.** The gateway is a stateless-to-the-outside WhatsApp engine; the
  frontend is the system of record for *people* (accounts, roles, login). Either can be
  redeployed without touching the other's storage.
- Programmatic WhatsApp over whatsmeow for DMs + groups: text, replies, mentions, reactions,
  edit, delete (revoke), polls (+ vote events), location, contact cards.
- Multi-user + **collaboration**: users self-register (toggleable) into a personal
  **organization**, attach number(s) to an org, consume events programmatically, and **invite
  others to co-manage** a connection (better-auth organizations). Platform admins manage everyone.
- WhatsApp auth via QR or pairing code.
- Two delivery mechanisms: the router's ticket-authenticated **WebSocket** realtime endpoint and
  **webhooks** (HMAC-signed, retried). The legacy gateway NDJSON endpoint is removed.
- REST send API; **two caller identities** — browser users (JWT) and programmatic clients
  (better-auth API keys) — accepted and verified at the router front door.
- **MySQL** for messages + a rich identity/contacts model **and** better-auth's tables; both
  the frontend and the gateway read it. **SQLite** (gateway-local, persistent volume) for the
  whatsmeow keystore.
- Read-only WhatsApp viewer; realtime dashboard fed by the router WebSocket over shared Redis.
- Docker: router, gateway, and frontend ship as separate roles; compose wires current MySQL/Redis
  dependencies and the gateway's persistent keystore volume.

**Designed-for-later (not built in v2, but the seams exist):** **multiple gateways** (a
session is pinned to the gateway that holds its keystore — schema carries `gateway_id` + a
`gateways` registry so sharding is additive). WhatsApp-as-login (`amlogin`).

**Non-goals (v2):** media download/upload (inbound media = metadata only; media sends →
`501`); per-session proxy; Business labels; horizontal scaling of a *single* session.

---

## 2. Architecture

The implemented runtime has a public router in front of the frontend-issued identity and private
HTTP gateways. The private HTTP proxy/assertion seam is transitional until gRPC replaces it.

```
 Browser / SDK ── REST + WebSocket ──► ROUTER (public front door)
                                       • JWT/API-key auth + CORS
                                       • generated Huma OpenAPI
                                       • ticketed WebSocket + control subscriber
                                       • session placement/routing
                                                  │
                              private HTTP proxy + Ed25519 assertion
                              (transitional; replaced by private gRPC/mTLS)
                                                  ▼
                                       GATEWAY (whatsmeow engine) ◄─ws─► WhatsApp
                                       • WA pipelines + webhooks
                                       • MySQL/Redis runtime (until cutover)
                                       • local SQLite keystore

 Frontend (TanStack Start + better-auth) ── issues JWT/JWKS, publishes ctrl:*
             │
             └── read-only display queries ──► MySQL ◄── router/gateway runtime

 Gateway event publication ──► shared Redis evt:* ──► router WebSocket
```

- **Gateway HTTP framework:** `go-chi/chi` (unchanged from v1).
- **Frontend:** TanStack Start (Vite + TanStack Router + server functions / API routes),
  shadcn components copied over from the v1 SPA, TanStack Query for data, better-auth for
  identity.
- **Trust direction:** the frontend is the *issuer*; the **router** is the *verifier* and the
  system's **single trust boundary** + front door. The router never calls the frontend on the
  request hot path (it caches the JWKS and reads the shared DB directly), then brokers each call to
  the gateway that owns the session. The gateway itself authenticates nothing end-user-facing — it
  trusts the router's signed internal assertion. (Central router, Increment A — see
  [`specs/router.md`](../specs/router.md) and [`plan-router-impl.md`](./plan-router-impl.md). The
  WebSocket cutover and generated Huma OpenAPI are implemented.)

---

## 3. Components & responsibilities

### Target control-plane boundary (migration in progress)

The eventual **API** owns public REST/Huma/OpenAPI, public gRPC, authentication and authorization,
application services, MySQL repositories, Redis jobs/realtime, gateway placement, webhook delivery,
and gateway enrollment. A private **gateway** owns whatsmeow clients, gateway-local device keys,
live WhatsApp operations, a local command ledger, and an acknowledged event journal. User tokens,
roles, database credentials, and Redis credentials do not cross into the final gateway process.

Initial supported deployments require each gateway engine endpoint to be addressable from the API;
normal commands use pooled unary HTTP/2 gRPC connections, while the gateway opens a long-lived
control/event stream. An outbound-only reverse-command transport is deferred. Public and private
protobufs are separate Buf modules with `FILE` compatibility policy and generated-code drift checked
from a temporary directory. These are Increment 0 decisions only; the following table continues to
describe the current runtime.

| Concern | Owner | Notes |
|---|---|---|
| Login / register / password / TOTP | **Frontend** (better-auth) | replaces all of Authula |
| Roles (`super_admin`/`user`), ban, impersonation, user CRUD, session revoke | **Frontend** (better-auth **admin** plugin) | `/api/auth/admin/*` |
| JWT issuance + JWKS | **Frontend** (better-auth **jwt** plugin) | `/api/auth/token`, `/api/auth/jwks` |
| API keys (create/list/revoke, permissions, expiry, rate-limit; **org-scoped**) | **Frontend** (better-auth **api-key** plugin) | router verifies them |
| Organizations, members, invitations (co-manage a connection) | **Frontend** (better-auth **organization** plugin) | `/api/auth/organization/*` |
| Admin/user dashboard, viewer, contacts UI | **Frontend** | reads MySQL via Drizzle for display, calls router for actions |
| WhatsApp clients (whatsmeow), pairing, reconnect | **Gateway** | one client per number, in-process |
| Inbound normalize/capture/persist/fan-out | **Gateway** | writes WA-domain tables |
| Outbound send, idempotency, rate limit, outbox | **Gateway** | Redis-backed async |
| Webhooks (dispatch, HMAC, retries, dead-letter) | **Gateway** | config surfaced in FE, mutated through router REST |
| WebSocket realtime | **Router** | scoped single-use ticket; shared Redis replay/tail |
| whatsmeow keystore | **Gateway** | SQLite, local persistent volume |
| Sole **writer** of WA-domain tables | **Gateway** | frontend reads them, never writes |
| Sole **writer** of better-auth tables | **Frontend** | router reads `apikey` and verifies JWTs; gateway trusts only the internal assertion |

**Single-writer rule.** Each domain has exactly one writer. The frontend writes auth tables;
the gateway writes WA-domain tables. Cross-reads are allowed (the *hybrid* read pattern, §6),
cross-writes are not. This keeps caches coherent and avoids two writers racing on the same
rows.

---

## 4. Trust & auth model (JWKS + API keys)

This is the heart of the v2 design and the answer to "how does the platform know a request is
legitimate?". There are exactly **two** caller identities, both verified with **no per-request
callback to the frontend**.

> **Central router (Increment A) — the trust boundary moved.** This verification now runs at the
> **router** (the single front door + trust boundary), not at each gateway. The router authenticates
> the caller, then brokers the request to the owning gateway carrying a request-bound Ed25519
> internal assertion the gateway verifies. Read the two-acceptor description below as "what the
> router does"; the gateway only trusts the router's assertion. Details:
> [`specs/router.md`](../specs/router.md), [`specs/trust-model.md`](../specs/trust-model.md).

### 4.1 Humans — better-auth JWT verified via JWKS

1. The frontend runs the better-auth **jwt** plugin. It exposes a **JWKS** at
   `GET {FRONTEND_URL}/api/auth/jwks` and mints short-lived (**5 min**, configurable) **asymmetric** JWTs
   (**EdDSA/Ed25519** by default; ES256/RS256 available) at `GET /api/auth/token`. The private
   key sits in better-auth's `jwks` table (encrypted at rest); the router only ever sees
   public keys.
2. On boot (and on a refresh interval / on unknown `kid`), the router fetches and **caches**
   the JWKS. It verifies every incoming JWT **locally** with a Go JOSE library
   (`github.com/lestrrat-go/jwx/v2`): signature against the matching `kid`, plus `iss`/`aud`
   == `BETTER_AUTH_URL`, plus expiry.
3. The token payload is customized (`definePayload`) to carry `sub` (user id),
   `activeOrganizationId`, and the member's **org role** (owner/admin/member) — plus the
   platform `role` (for `super_admin`). After verification the router has identity, the active
   org, **and** RBAC with zero shared secrets and zero round-trips on the hot path. (better-auth
   doesn't auto-include the active org in the token, so `definePayload` adds it explicitly.)

```
Authorization: Bearer <better-auth JWT>      # browser / dashboard
```

**Where the browser gets the JWT:** the TanStack Start server (which holds the better-auth
session cookie) requests a token from better-auth and hands it to the client. The browser presents
it to the router for REST and realtime-ticket minting; the WebSocket itself redeems the scoped,
single-use ticket.

### 4.2 Machines — better-auth API keys verified against the shared DB

Programmatic clients authenticate with a better-auth **api-key** plugin key:

```
Authorization: Bearer <api-key>     # or  x-api-key: <api-key>
```

The frontend's UI creates/lists/revokes keys (with permissions, expiry, rate limits). The
router **validates locally against the shared `apikey` table** by hashing the presented key with
better-auth's scheme and looking up the row,
then checking `enabled` / `expiresAt` / `permissions`. Keys are **org-scoped**
(`organizationId`): a key acts within exactly one organization, and the router resolves the
owning org from the key row (the API-key path needs no JWT). This avoids a frontend callback on
the request path. Validated keys are cached at the router and revoked via its Redis control-bus
subscriber — see §4.6.

> **Pinned trust seam:** better-auth 1.6.22's deterministic API-key hash is replicated in Go and
> covered by a contract test. A version upgrade must rerun that test; the supported remote verify
> endpoint remains a fallback.

### 4.3 Router auth middleware

One middleware, two acceptors, evaluated in order:

1. `Authorization: Bearer` that parses as a JWT → verify via JWKS → `{user_id,
   organization_id (active), org_role, platform role}`.
2. Otherwise treat the bearer / `x-api-key` as an **api-key** → verify vs `apikey` table →
   `{organization_id, key permissions}` (no user).
3. Neither → `401`.

This middleware runs at the **router**, not the gateway. The resolved principal is put on the
request context; router resolution and gateway handlers authorize **per-resource by
`organization_id`** — a caller sees only resources owned by their active org. Within the org,
the **member role** (owner/admin manage; member read/send — tunable via the org plugin's
access control) and **api-key permissions** `{read,send,manage,events}` gate the action. A
platform `super_admin` (better-auth admin role, carried in the JWT) can cross orgs for
oversight.

### 4.4 CORS & deployment

Browsers call the router for REST actions and ticketed WebSocket realtime. The **router** owns CORS
for `FRONTEND_ORIGINS`; gateways do not expose public CORS or accept browser credentials.
Server-to-server programmatic callers also enter through the router and are origin-agnostic.

### 4.5 Multi-gateway routing

JWT and API-key verification run once at the router. A WhatsApp session lives in exactly one
gateway's SQLite keystore, so `wa_sessions.gateway_id` pins it and the `gateways` registry drives
router placement and session-owner routing. The router forwards a request-bound Ed25519 assertion;
gateways never need the public caller credential. This private HTTP seam is transitional to pooled
gRPC/mTLS commands and desired-state assignment fencing.

### 4.6 API-key cache & instant revocation

The router keeps a small **positive cache** of validated keys, so a busy client isn't a
MySQL lookup per request and a brief DB blip doesn't drop in-flight callers:

```
cache[ key_hash ] = { keyId, userId, scopes, expiresAt }    # TTL ~60s, fail-closed
```

indexed so it can evict by `keyId` and by `userId`. The short TTL is the **backstop**: even if
the router misses a notification, a revoked key stops working within the window (the `apikey`
row is gone, so the next refresh fails closed).

**Instant revocation (chosen)** rides a **cross-service Redis control bus** shared by the
frontend publisher and router subscriber:

1. A user revokes key K (or is banned) in the dashboard → the frontend deletes/disables the
   `apikey` row via better-auth (a MySQL write).
2. In the same handler (or a better-auth `after` hook) the frontend publishes:
   ```
   PUBLISH ctrl:apikey.revoked  {"keyId":"…","userId":"…","ts":…}
   ```
3. The router subscribes to `ctrl:*`. On `apikey.revoked` it evicts the matching cache entry and
   closes affected live WebSockets. A reconnect re-validates against MySQL (row gone) → `401`.
4. `ctrl:user.banned {userId}` does the same for **all** of a user's keys and live streams at
   once — and can feed a short JWT **deny-list** (TTL = max JWT lifetime) so the user's
   in-flight JWTs are rejected before their 5-min expiry. `ctrl:member.removed
   {userId, organizationId}` is the org-scoped version — when a collaborator is removed from an
   org, the router drops their access to *that* org's streams/resources (deny `(userId, orgId)`
   until JWT TTL ages out), without touching their other orgs.

> **Router-owned revocation.** The frontend publishes control events without knowing gateway
> placement. The router owns the public credential cache and all browser WebSockets, so one
> subscriber performs eviction and connection drop.

**Two Redis roles** — collapsible to one instance, splittable later:

| Role | Env | Carries | Who connects |
|---|---|---|---|
| **Work/realtime** | `REDIS_URL` | asynq queue, rate-limit buckets, idempotency, gateway `evt:*` publication and router WebSocket fan-out | gateways + router |
| **Control bus** | `PUBSUB_REDIS_URL` (defaults to `REDIS_URL`) | low-volume `ctrl:*` pub/sub (key/user revocation, bans) | **frontend** (publish) + router (subscribe) |

- **Single instance (dev / single server):** leave `PUBSUB_REDIS_URL` unset → it falls back to
  `REDIS_URL`; one Redis does everything.
- **Split (prod / multi-gateway):** point `PUBSUB_REDIS_URL` at a shared, possibly-managed
  Redis (e.g. Upstash) reachable by the frontend and router; gateways still need the current
  shared event/work Redis until acknowledged gRPC ingest replaces their Redis dependency. The
  frontend's **only** Redis dependency is publish access to the control bus.

**No collisions on a shared instance.** Namespace by **key/channel prefix**, not Redis DB
number (managed Redis like Upstash often disallows `SELECT`/multiple DBs):

- work keys → `gw:…` (per-gateway state under `gw:{GATEWAY_ID}:…`); asynq keeps its `asynq:` prefix.
- realtime fan-out channels → `evt:{organization}:{session}` (plus scoped wildcards).
- control-bus channels → `ctrl:apikey.revoked`, `ctrl:user.banned`, `ctrl:member.removed`.
- a `REDIS_PREFIX` env isolates multiple independent stacks on one Redis.

> **Delivery semantics:** Redis pub/sub is fire-and-forget — a router replica that's down when a
> control message is published misses it, which is exactly what the 60-s TTL backstop covers. If you
> later need at-least-once (a just-restarted gateway catching up), promote `ctrl:*` from
> pub/sub to a Redis **Stream** with consumer groups; the call sites keep the same shape.

**Boot reconciliation (catch up on anything missed while down).** The in-memory cache is cold
after a restart, so no stale *cached* key survives a reboot. The real catch-up is over
**persistent** authorizations, done as a one-time sweep on startup:

1. Before the Session Manager resumes each WhatsApp session from the keystore, the gateway
   checks the session's **owning org still exists and is enabled** in MySQL and **skips +
   marks `STOPPED`** any whose org was deleted/disabled while it was down (orphan guard).
The router's positive key cache is cold after its own restart, so the next public request validates
against the authoritative `apikey` row. Gateway orphan reconciliation is separate lifecycle safety,
not public-auth cache recovery.

### 4.7 JWT lifecycle, refresh & revocation

better-auth has **no separate refresh token** — the **session is the long-lived, revocable
credential**, and the JWT is a short-lived access token minted from it. That's exactly the
"revocable refresh token + short access token" split, with the `session` row playing the
refresh-token role.

- **Mint / refresh:** the browser (holding the better-auth **session cookie**, httpOnly, on
  the *frontend's* domain) calls `{FRONTEND_URL}/api/auth/token` to get a fresh JWT. To
  refresh, it just calls again. Set `expirationTime` to **5 min** — short enough that the
  revocation window is tiny, long enough to avoid hammering the token endpoint.
- **Revoke (blocks refresh):** revoke the **session** — better-auth `admin`
  (`/api/auth/admin/revoke-user-session` / `revoke-user-sessions`), or the user logs out.
  Once the session is gone, `/api/auth/token` stops minting, so the client can no longer
  refresh; access ends within ≤ the JWT TTL. This is the "revocable refresh token" the design
  wants.
- **Instant kill (in-flight JWTs):** session revocation doesn't retroactively invalidate a
  JWT that's already minted — it lives until expiry. For *immediate* cutoff, publish
  `ctrl:user.banned {userId}` (or `ctrl:session.revoked {sessionId}`) on the control bus
  (§4.6); the router rejects the affected principal and drops the user's live WebSockets. So:
  **session-revoke = automatic refresh block;
  control-bus = optional instant in-flight kill.**
- **Realtime vs short JWTs:** the JWT authorizes creation of a short-lived, single-use realtime
  ticket. The WebSocket redeems that ticket and resumes via the stored `since` cursor; live
  revocation is enforced by the router's control-bus connection drop.

---

## 5. Session model (single-instance per session)

- One `whatsmeow.Client` per attached number, holding a **live WebSocket in-process** on its
  owning gateway. Sessions are stateful; they can't be statelessly load-balanced. A session is
  pinned to one gateway via `gateway_id`.
- A **Session Manager** owns `map[sessionID]*ManagedSession`, loads devices from the (SQLite)
  keystore on boot, reconnects with backoff+jitter.
- Statuses: `STARTING · SCAN_QR_CODE · WORKING · FAILED · STOPPED · LOGGED_OUT`.
- `LoggedOut` / stream-replaced / ban → mark `LOGGED_OUT`/`FAILED`, **stop** reconnect, emit
  `session.status`.

---

## 6. Storage planes & data ownership

Three planes. The first two share one MySQL instance; the third is gateway-local.

| Plane | Contents | Backend | Writer | Readers |
|---|---|---|---|---|
| **Auth** | `user`, `session`, `account`, `verification`, `jwks`, `apikey`, `organization`, `member`, `invitation`, two-factor + admin tables | **MySQL** | Frontend (better-auth) | Frontend (rw); router reads `apikey` to verify |
| **WA app data** | `wa_sessions`, `gateways`, `messages`, `chats`, identities/contacts, groups, `webhooks`, `webhook_deliveries`, `outbox`, `event_log` | **MySQL** | Gateway | Gateway (rw); Frontend (read-only, for dashboards) |
| **whatsmeow keystore** | device identities, Signal sessions, prekeys, sender keys, app-state, LID map (`wmstore_*`) | **SQLite** (gateway-local, **persistent volume**) | Gateway | Gateway only |

### 6.1 Keystore on SQLite (the change from v1)

v1 ran a custom **MySQL** whatsmeow store. v2 uses **SQLite**, which whatsmeow's `sqlstore`
supports natively (zero custom adapter) and is the better-trodden path. Use a **pure-Go**
driver (`modernc.org/sqlite`) so the gateway keeps building with `CGO_ENABLED=0` and stays a
small static image.

```
WHATSMEOW_STORE_DSN=file:/data/keystore/store.db?_pragma=foreign_keys(on)&_pragma=journal_mode(WAL)
```

**Persistence:** the SQLite file holds device crypto material — losing it = re-pairing every
number. Mount `/data/keystore` on a **named Docker volume** (§15). Back it up. Because it's
gateway-local, it's also what pins a session to a gateway (§4.5).

### 6.2 Hybrid reads (the frontend ↔ MySQL relationship)

The frontend has direct, **read-only** access to the WA app-data tables and uses it for fast
dashboard/viewer/contacts rendering (TanStack Start server functions querying MySQL). It does
**not** write them. The frontend's DB layer is **Drizzle**: better-auth runs on the
`drizzleAdapter`, and the *same* Drizzle client serves the read-only WA queries. The WA tables
are modeled as **read-only** Drizzle definitions mirroring the gateway-owned schema — generate
them with `drizzle-kit introspect` (pull) against the gateway-migrated DB so they can't drift. For anything that changes WhatsApp or gateway state — send a message,
start/stop a session, fetch a QR, register a webhook — the frontend/browser calls the **router REST
API**, which currently reverse-proxies to the owning gateway. Realtime comes from the router's
ticketed WebSocket over shared Redis. So:

- **Display data** → frontend reads MySQL directly (low latency, no extra hop).
- **Actions / mutations** → browser/frontend → router → owning gateway (gateway is currently the
  WA-data writer; the proxy seam is transitional to gRPC).
- **Realtime** → browser → router ticket + WebSocket; gateway events arrive over shared Redis.

This is the literal expression of "both read from MySQL, gateway handles keystore itself."

---

## 7. Data model (MySQL DDL — WA app-data plane)

Conventions: `utf8mb4`/`utf8mb4_unicode_ci`; timestamps = **epoch ms** `BIGINT`; surrogate
`BIGINT UNSIGNED AUTO_INCREMENT` PKs. **Ownership is by `organization_id`** = a better-auth
**organization** id; a user reaches a resource through org **membership** (role
owner/admin/member). Every user gets a **personal organization** on signup (auto-created), so
solo use is just a one-member org and "sharing a WhatsApp connection" = inviting someone into
the org. `created_by_user_id` is kept for audit. The v1 `tenants` mirror and custom `api_keys`
table are **removed**.

> better-auth's own tables (`user`, `session`, `account`, `verification`, `jwks`, `apikey`,
> **`organization`, `member`, `invitation`**, two-factor/admin tables) are defined as a
> **Drizzle** schema (produced by `npx @better-auth/cli generate`) and migrated with
> **drizzle-kit** (`generate` → `migrate`; the better-auth `migrate` CLI is Kysely-only and not
> used). They are **not** redefined here — the router treats the verification subset as read-only external schema. Match
> `organization_id`/`user_id` lengths to better-auth's ids (`VARCHAR(64)` is a safe default).
> The router authenticates/authorizes from **JWT claims** (`activeOrganizationId` + member role) and
> API-key `organizationId`; gateways receive the resulting private assertion during the HTTP
> transition.
>
> **Session-scoped tables** (`chats`, `messages`, `*_contacts`, `*_group_members`, `poll_votes`)
> stay keyed by `session_id`; their owning org resolves via `wa_sessions`. Denormalize
> `organization_id` onto them only if direct frontend reads need to filter by org without a
> join (optional read-perf optimization).

```sql
-- Registry of gateways. One self-row in v2; rows added when sharding (forward-compat).
CREATE TABLE gateways (
  id            VARCHAR(64) PRIMARY KEY,            -- = GATEWAY_ID
  label         VARCHAR(255) NULL,
  base_url      TEXT NULL,                          -- private address where the router reaches this gateway
  last_seen_at  BIGINT NULL,
  created_at    BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wa_sessions (
  id                 VARCHAR(64) PRIMARY KEY,       -- app session id (ULID)
  organization_id    VARCHAR(64) NOT NULL,          -- = better-auth organization id (owner)
  created_by_user_id VARCHAR(64) NULL,              -- better-auth user id (audit: who created it)
  gateway_id         VARCHAR(64) NOT NULL,          -- which gateway holds this session's keystore
  label             VARCHAR(255) NULL,
  status            ENUM('starting','scan_qr_code','working','failed','stopped','logged_out') NOT NULL DEFAULT 'stopped',
  wa_jid            VARCHAR(255) NULL,
  wa_lid            VARCHAR(255) NULL,
  phone_number      VARCHAR(64) NULL,
  is_admin_session  TINYINT(1) NOT NULL DEFAULT 0,  -- the WHATSAPP_ADMIN_NUMBER session
  auto_read         TINYINT(1) NOT NULL DEFAULT 1,
  presence_typing   TINYINT(1) NOT NULL DEFAULT 0,
  rate_per_min      INT NOT NULL DEFAULT 20,
  rate_per_hour     INT NOT NULL DEFAULT 200,
  last_connected_at BIGINT NULL,
  created_at        BIGINT NOT NULL,
  updated_at        BIGINT NOT NULL,
  KEY idx_sessions_org (organization_id),
  KEY idx_sessions_gateway (gateway_id),
  UNIQUE KEY uq_sessions_jid (wa_jid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE webhooks (
  id              VARCHAR(64) PRIMARY KEY,
  organization_id VARCHAR(64) NOT NULL,
  session_id      VARCHAR(64) NULL,                 -- null = all the org's sessions
  url            TEXT NOT NULL,
  events         JSON NOT NULL,                     -- ["message","poll.vote"] or ["*"]
  hmac_secret    VARBINARY(512) NULL,               -- AES-GCM encrypted at rest
  custom_headers JSON NULL,
  retry_policy   JSON NOT NULL,                     -- {"policy":"exponential","delaySeconds":2,"attempts":15}
  active         TINYINT(1) NOT NULL DEFAULT 1,
  created_at     BIGINT NOT NULL,
  updated_at     BIGINT NOT NULL,
  KEY idx_webhooks_org (organization_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE webhook_deliveries (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  webhook_id    VARCHAR(64) NOT NULL,
  event_id      VARCHAR(64) NOT NULL,
  status        ENUM('pending','delivered','failed','dead') NOT NULL DEFAULT 'pending',
  attempts      INT NOT NULL DEFAULT 0,
  response_code INT NULL,
  next_retry_at BIGINT NULL,
  last_error    TEXT NULL,
  created_at    BIGINT NOT NULL,
  KEY idx_deliv_retry (status, next_retry_at)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- ===== Identity / Contacts model (global; not user-scoped) =====

CREATE TABLE whatsapp_identities (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  lid           VARCHAR(255) NOT NULL,
  phone_number  VARCHAR(64)  NULL,
  phone_jid     VARCHAR(255) NULL,
  name          TEXT NULL,                          -- push name (preferred display)
  business_name TEXT NULL,
  first_seen_at BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL,
  UNIQUE KEY uq_identity_lid (lid),
  KEY idx_identity_phone (phone_jid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE whatsapp_contacts (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id    VARCHAR(64) NOT NULL,               -- the account that encountered them
  lid           VARCHAR(255) NOT NULL,
  seen_in_dm    TINYINT(1) NOT NULL DEFAULT 0,
  dm_first_seen_at BIGINT NULL,
  dm_last_seen_at  BIGINT NULL,
  message_count BIGINT NOT NULL DEFAULT 0,
  first_seen_at BIGINT NOT NULL,
  last_seen_at  BIGINT NOT NULL,
  UNIQUE KEY uq_contact (session_id, lid),
  KEY idx_contact_lid (lid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE whatsapp_groups (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  group_jid     VARCHAR(255) NOT NULL,
  subject       TEXT NULL,
  description   TEXT NULL,
  owner_jid     VARCHAR(255) NULL,
  participant_count INT NULL,
  is_announce   TINYINT(1) NULL,
  is_locked     TINYINT(1) NULL,
  created_at_wa BIGINT NULL,
  first_seen_at BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL,
  UNIQUE KEY uq_group_jid (group_jid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE whatsapp_group_members (
  id             BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id     VARCHAR(64) NOT NULL,
  group_jid      VARCHAR(255) NOT NULL,
  lid            VARCHAR(255) NOT NULL,
  group_nickname TEXT NULL,
  role           ENUM('member','admin','superadmin') NOT NULL DEFAULT 'member',
  first_seen_at  BIGINT NOT NULL,
  last_seen_at   BIGINT NOT NULL,
  UNIQUE KEY uq_group_member (session_id, group_jid, lid),
  KEY idx_gm_group (group_jid),
  KEY idx_gm_lid (lid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- ===== Messages & chats ===== (unchanged from v1)

CREATE TABLE chats (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id    VARCHAR(64) NOT NULL,
  chat_jid      VARCHAR(255) NOT NULL,
  type          ENUM('dm','group','newsletter','broadcast','status') NOT NULL,
  name          TEXT NULL,
  last_message_at BIGINT NULL,
  unread_count  INT NOT NULL DEFAULT 0,
  archived      TINYINT(1) NOT NULL DEFAULT 0,
  pinned        TINYINT(1) NOT NULL DEFAULT 0,
  muted_until   BIGINT NULL,
  UNIQUE KEY uq_chat (session_id, chat_jid),
  KEY idx_chat_recent (session_id, last_message_at)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE messages (
  id                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id        VARCHAR(64) NOT NULL,
  wa_message_id     VARCHAR(255) NOT NULL,
  chat_jid          VARCHAR(255) NOT NULL,
  sender_lid        VARCHAR(255) NULL,
  sender_jid        VARCHAR(255) NULL,
  from_me           TINYINT(1) NOT NULL DEFAULT 0,
  direction         ENUM('in','out') NOT NULL,
  type              VARCHAR(32) NOT NULL,
  body              MEDIUMTEXT NULL,
  quoted_message_id VARCHAR(255) NULL,
  mentions          JSON NULL,
  has_media         TINYINT(1) NOT NULL DEFAULT 0,
  media_meta        JSON NULL,                       -- NOT downloaded in v2
  status            ENUM('pending','sent','delivered','read','played','failed') NULL,
  ack_level         INT NULL,
  error             TEXT NULL,
  edited            TINYINT(1) NOT NULL DEFAULT 0,
  deleted           TINYINT(1) NOT NULL DEFAULT 0,
  timestamp         BIGINT NOT NULL,
  raw_json          JSON NULL,
  created_at        BIGINT NOT NULL,
  UNIQUE KEY uq_msg (session_id, wa_message_id),
  KEY idx_msg_chat (session_id, chat_jid, timestamp),
  KEY idx_msg_sender (sender_lid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE poll_votes (
  id               BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id       VARCHAR(64) NOT NULL,
  poll_message_id  VARCHAR(255) NOT NULL,
  voter_lid        VARCHAR(255) NOT NULL,
  selected_options JSON NOT NULL,
  timestamp        BIGINT NOT NULL,
  raw_json         JSON NULL,
  KEY idx_pollvote (session_id, poll_message_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE outbox (
  id              VARCHAR(64) PRIMARY KEY,            -- ULID
  organization_id VARCHAR(64) NOT NULL,
  session_id      VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(255) NULL,
  payload         JSON NOT NULL,
  status          ENUM('queued','sending','sent','failed') NOT NULL DEFAULT 'queued',
  attempts        INT NOT NULL DEFAULT 0,
  wa_message_id   VARCHAR(255) NULL,
  error           TEXT NULL,
  created_at      BIGINT NOT NULL,
  updated_at      BIGINT NOT NULL,
  UNIQUE KEY uq_idem (organization_id, idempotency_key)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE event_log (
  id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,  -- monotonic cursor
  event_id    VARCHAR(64) NOT NULL,                         -- ULID, exposed to clients
  organization_id VARCHAR(64) NOT NULL,
  session_id  VARCHAR(64) NOT NULL,
  type        VARCHAR(64) NOT NULL,
  payload     JSON NOT NULL,
  created_at  BIGINT NOT NULL,
  KEY idx_event_cursor (organization_id, session_id, id),
  UNIQUE KEY uq_event_id (event_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

**Retention:** daily prune of `event_log`/`messages`/`webhook_deliveries` older than
`RETENTION_DAYS` (`0` = keep forever).

**Migrations are rewritten from scratch for v2.** The project is **pre-release**, so there is
**no data to preserve and no in-place migration**: drop the v1 `migrations/` (`0001_init`,
`0002_wmstore`) and author fresh v2 migrations for the schema above, against an empty database.
Differences vs v1 (orientation only, *not* a backfill): no `tenants`/`api_keys`; ownership is
`organization_id` (+`created_by_user_id`) instead of `tenant_id`; new `gateways` +
`wa_sessions.gateway_id`; the `wmstore_*` keystore tables leave MySQL entirely (now SQLite,
auto-migrated by whatsmeow's `sqlstore`). better-auth's CLI owns `organization`/`member`/
`invitation` + the rest of the auth plane.

---

## 8. WhatsApp lifecycle, pairing & the admin number

**Pairing:** QR — `Connect()` on an empty device emits `QREvent`; stream the QR string
(refreshes ~20s) for the UI. Pairing code — `PairPhone(ctx, phone, true, PairClientChrome,
name)` returns a linking code; exposed via `POST /sessions/{id}/pairing-code`.

**Admin number bootstrap:** `WHATSAPP_ADMIN_NUMBER` (gateway env) declares the system admin
number. On boot, if no valid keystore session exists for it, the gateway creates an
`is_admin_session=1` session (owned by the configured super-admin user id,
`GATEWAY_ADMIN_USER_ID`, or left system-owned) and **prints the pairing code to console +
surfaces it in the admin panel**. It does double duty as a normal API-usable number.
`WHATSAPP_ADMIN_CMD_PREFIX=am` — inbound admin-session messages starting with the prefix
(e.g. `amlogin 123456`) route to the internal **command interceptor** (§9) and are **not**
persisted/emitted/counted. v2 ships the interceptor + a no-op registry; `amlogin` is later.

---

## 9. Inbound pipeline

Ordered stages per whatsmeow event (tagged with session/org):

1. **Normalize** → versioned envelope (§11); never expose raw protobufs.
2. **Command interceptor** (admin session) → if inbound and body starts with
   `WHATSAPP_ADMIN_CMD_PREFIX`, hand to the command registry and **drop**.
3. **Identity/contacts capture** → upsert `whatsapp_identities` (push name, phone/LID); upsert
   `whatsapp_contacts` for this account (set `seen_in_dm` + DM timestamps for DMs; bump
   `message_count`/`last_seen_at`). Groups → upsert `whatsapp_groups` +
   `whatsapp_group_members` (with `group_nickname`, role). Prefer push name for display.
4. **Persist** → upsert `chats`; insert `messages` (+`raw_json`); insert `poll_votes` on
   `DecryptPollVote`; update `messages.status`/`ack_level` on receipts.
5. **Auto-read** (if `auto_read`) → send read receipt before any reply; optional
   `presence_typing` "composing" before outbound replies.
6. **Fan-out** → Redis pub/sub (stream subscribers) + enqueue webhook deliveries + append
   `event_log`.

Source-level `ignore` rules skip persistence/fan-out for status/groups/channels/broadcast as
configured.

---

## 10. Outbound pipeline

**Unified send** with a typed body. Modes: **sync** (default; block on whatsmeow ack, return
status + `wa_message_id`) and **async** (`?async=true`; persist to `outbox`, return `202` +
`outbox_id`, final status via `message.status`; Redis-backed `asynq`). **Idempotency:**
`Idempotency-Key` header, unique per user; replays return the original result. **Rate
limiting:** per-session token bucket in Redis (`rate_per_min`/`rate_per_hour`); over-limit →
`429` (sync) or deferred (async); optional jittered pacing.

Supported types: `text`, `poll`, `poll_vote`, `location`, `contact`, plus message ops
(reaction/edit/revoke/forward) as sub-resources (§13). Media types parse but return `501`.

---

## 11. Eventing & event schema

Same envelope on realtime and webhook delivery.

**Router WebSocket:** authenticate `POST /api/v1/realtime/ticket` with JWT/API key and request a
session/org/firehose scope plus event filters and optional `since`. The router stores the resolved
scope in a short-lived Redis ticket. `GET /api/v1/realtime?ticket=…` atomically redeems it, replays
`event_log`, then tails shared Redis `evt:*` using WebSocket text frames. The legacy gateway NDJSON
endpoint is removed. The gRPC migration later feeds realtime only after acknowledged event commit.

**Webhooks (server-to-server):** per org/session or global. Headers: `X-Webhook-Request-Id`,
`X-Webhook-Timestamp`; with HMAC `X-Webhook-Hmac` + `X-Webhook-Hmac-Algorithm: sha512`; plus
`customHeaders`. Retries per `retry_policy`, tracked in `webhook_deliveries`, exhausted →
`dead`; dedup by `event_id`. `events:["*"]` for all.

**Envelope (versioned):**
```json
{ "schema":"v1","id":"evt_01J9…","event":"message","session":"sess_01J8…",
  "org":"org_abc","timestamp":1719400000000,"payload":{} }
```

**Catalog:** `session.status` · `auth.qr` · `auth.code` · `message` · `message.from_me` ·
`message.status` · `message.reaction` · `message.edited` · `message.revoked` · `poll.vote` ·
`presence.update` · `group.update` · `group.participant` · `chat.update` · `contact.update` ·
`call.incoming` · `newsletter.update`. `media` is always metadata-only (`hasMedia:true,
media:null`).

---

## 12. Frontend (TanStack Start + better-auth)

A fullstack TanStack Start app. Replaces the v1 embedded React Router SPA. **shadcn
components are copied over** from `web/app/components/ui/*` (Radix-based, framework-agnostic).
The API client targets the router and the realtime client uses its ticketed WebSocket.

**Porting from the v1 SPA (reshape, don't lift).** v1 is a **client-only SPA** (CSR, embedded
in the Go binary); v2 is **fullstack TanStack Start** (SSR + server functions). Reuse the
*logic*, but re-fit it to TanStack Start's idioms rather than copying the SPA wiring verbatim:

- **Data fetching** → route **loaders** + **server functions** (`createServerFn`), not
  client-only `useEffect`/fetch. Initial/SSR reads run **server-side** (Drizzle direct reads
  §6.2); the client hydrates from loader data and uses TanStack Query for
  mutations + the router WebSocket for realtime.
- **Auth** → **server middleware** (`createMiddleware`) + route `beforeLoad` that resolve the
  better-auth session, attach `{user, activeOrg, role}`, gate routes, and mint the router-facing JWT —
  replacing v1's client-side route guards.
- **Routing** → TanStack Router **file-based** routes (route tree, `beforeLoad`/`loader`/
  context), re-mapping the v1 React Router route objects.
- **Reused ~as-is:** shadcn `components/ui`, event-bus logic, Zod schemas, and generated API types;
  the old NDJSON parser was replaced by the router WebSocket client.

**Identity (better-auth on MySQL via the Drizzle adapter):** mounted at `/api/auth/*`.
`drizzleAdapter(db, { provider: "mysql" })`; the auth-table Drizzle schema is generated by
`npx @better-auth/cli generate`. Plugins:

| Plugin | Role |
|---|---|
| Email & Password (core) | login + registration (gated by `USER_REGISTRATION_ENABLED`) |
| Two-Factor (`twoFactor`) | optional TOTP 2FA |
| Admin (`admin`) | roles `super_admin`/`user`; user CRUD, ban/unban, impersonation, session revoke — `/api/auth/admin/*` |
| API Key (`apiKey`) | programmatic keys with permissions/expiry/rate-limit (router verifies) |
| JWT (`jwt`) | short-lived JWTs + JWKS at `/api/auth/jwks`; payload carries `activeOrganizationId` + member role |
| Organization (`organization`) | orgs, members, roles, **email invitations**; a **personal org** auto-created per user on signup |

> Roles: **platform** `super_admin` bootstrapped via the `admin` plugin's `adminUserIds` (or
> the first user on seed) — cross-org oversight. **Org** roles owner/admin/member gate access
> *within* an org. `user` self-registers (toggleable), gets a personal org, attaches numbers to
> an org, manages that org's keys/webhooks, and can invite collaborators. A member sees data for
> the orgs they belong to (active org at a time).

**Server vs client (serverless-friendly — the router owns client realtime connections):** The
frontend server does **only short-lived work** — better-auth endpoints, **minting JWTs**
(`/api/auth/token`), and **direct MySQL reads via Drizzle** for SSR/loaders (§6.2). It does **not proxy
router traffic**. The **browser talks to the router directly** for actions and obtains a scoped
ticket for realtime — the router authenticates and brokers REST to the owning gateway
(central router, Increment A). This is what lets the frontend run on **serverless**
(Vercel/Cloudflare/Netlify), where a function can't hold a long-lived streaming proxy open: the
realtime endpoint lives in the router, and the browser connects to it directly.

- **Server** (serverless): auth, token mint, direct MySQL reads. No streaming, no proxy.
- **Client:** TanStack Query for data; router ticket mint + WebSocket for realtime. REST uses a
  refreshed bearer credential; WebSocket authentication is bound into the single-use ticket.
- *(Optional, non-serverless only:)* a long-running frontend host can proxy router REST calls
  server-side to avoid exposing a client JWT. Realtime still terminates at the router.

> **Serverless + MySQL:** direct reads from serverless functions can exhaust DB connections —
> use a pooler / serverless-friendly path (PlanetScale, RDS Proxy, or a driver with
> connection reuse). The gateway, a long-running process, keeps a normal pool.

**Realtime:** mint a router ticket and open `GET {ROUTER_URL}/api/v1/realtime?ticket=…`. Live
surfaces include session status, QR/pairing code, viewer messages, and the event monitor.
Auto-reconnect mints a new ticket with `since={lastEventId}`; polling remains a fallback.

**Surfaces:**
- **Admin** — users + orgs (better-auth admin plugin: list/ban/impersonate/roles), all WhatsApp
  sessions + statuses across **all orgs** (each showing its **gateway**), admin-number pairing
  code, event monitor.
- **User** (toggleable) — an **org switcher** (active org); that org's sessions
  (create/start/stop/QR/pairing), **each tagged with the gateway it lives on**; API keys
  (better-auth api-key UI), webhooks, viewer. **Members & invitations** UI is a fast-follow
  (backed by the org plugin endpoints — see §17/§18).
- **Viewer** (read-only) — chats + message timeline; media → "not downloaded" placeholder.
- **Contacts** — searchable found-users list; drill into DM + groups (names + per-group
  nicknames).

---

## 13. Public REST API (router)

**Principles:** resource-oriented; **one way to do a thing** (single send endpoint);
consistent response/error/pagination envelopes; **session is always a path param**; plural
nouns + standard verbs; non-CRUD actions as explicit sub-resources (`:start`, `/reaction`).
Base `/api/v1`. **Auth:** the router accepts JWT or API key (§4), resolves routing, and currently
proxies to private gateway handlers with an Ed25519 assertion. The gRPC migration moves application
logic into the API and replaces that private proxy. Errors:

```json
{ "error": { "code": "rate_limited", "message": "…", "details": {} } }
```

Lists: `?limit=&cursor=` (opaque cursor over `id`). Huma Go operations generate the contract of
record, `docs/openapi.yaml`, served by the router at `/api/v1/openapi.yaml`.

### Sessions
| Method | Path |
|---|---|
| POST | `/sessions` — create `{label,start?,autoRead?,presenceTyping?}` |
| GET / GET | `/sessions` · `/sessions/{id}` |
| POST | `/sessions/{id}:start` · `:stop` · `:restart` · `:logout` |
| DELETE | `/sessions/{id}` |
| GET | `/sessions/{id}/me` · `/sessions/{id}/qr` (`?format=image`) |
| POST | `/sessions/{id}/pairing-code` `{phone}` |

Session responses include **`gatewayId`** plus the gateway's `label`/`status`/`baseUrl` (from
the `gateways` registry) so the dashboard can show **where each session lives** — essential
once there's more than one gateway.

### Messages
| Method | Path | Notes |
|---|---|---|
| POST | `/sessions/{id}/messages` | send any type; `Idempotency-Key`, `?async` |
| PATCH | `/sessions/{id}/messages/{mid}` | edit text |
| DELETE | `/sessions/{id}/messages/{mid}` | revoke |
| POST | `/sessions/{id}/messages/{mid}/reaction` · DELETE to remove |
| POST | `/sessions/{id}/messages/{mid}/forward` `{to}` |
| POST | `/sessions/{id}/messages/{mid}/vote` `{options:[]}` |

Send body (discriminated on `type`): `text`, `poll`, `location`, `contact` as in v1;
`image|video|audio|document|sticker` → `501`.

### Chats · Contacts · Groups · Channels · Status · Presence
Unchanged from v1 (`/sessions/{id}/chats…`, `/contacts…`, `/groups…`, `/channels…`,
`/status`, `/presence`). Cross-account contact resolution `GET /contacts/{lid}` requires
`super_admin` (resolved from the JWT `role`).

### Events · Webhooks · Health
| POST / GET | `/realtime/ticket` · `/realtime?ticket=…` (router ticket + WebSocket, §11) |
| POST/GET/PATCH/DELETE | `/webhooks` · `/webhooks/{id}` (config; surfaced in FE) |
| GET | `/admin/sessions` — cross-org WhatsApp oversight (`super_admin`) |
| GET | `/healthz` · `/readyz` · `/metrics` (Prometheus) |

> **Removed vs v1:** `/auth/*` (→ better-auth), `/keys*` (→ better-auth api-key plugin),
> `/auth/admin/*` (→ better-auth admin plugin).

---

## 14. Configuration (ENV)

### Router (public front door)

The implemented router uses `ROUTER_HTTP_ADDR`, `ROUTER_PUBLIC_URL`, `MYSQL_DSN`, `REDIS_URL`,
`PUBSUB_REDIS_URL`, better-auth issuer/JWKS settings, `FRONTEND_ORIGINS`, and its Ed25519 assertion
key/issuer. It owns public auth, CORS, Huma OpenAPI, REST routing, and WebSocket realtime.

### Gateway (private HTTP engine; transitional to gRPC)
| Var | Default | Purpose |
|---|---|---|
| `HTTP_ADDR` | `:8080` | listen addr |
| `GATEWAY_ID` | `gw-1` | this gateway's id (rows in `gateways`, `wa_sessions.gateway_id`) |
| `PUBLIC_URL` | — | internal base URL registered for router proxying; not browser-facing |
| `ROUTER_JWKS_URL` | — | router assertion JWKS |
| `ROUTER_ASSERTION_ISSUER` | `router` | expected private assertion issuer |
| `APP_ENCRYPTION_KEY` | — | base64 32-byte AES-GCM key (webhook secrets at rest) |
| `MYSQL_DSN` | — | shared app-data DSN (WA tables rw; `apikey`/`user` ro) |
| `WHATSMEOW_STORE_DSN` | `file:/data/keystore/store.db?...` | **SQLite** keystore (persistent volume) |
| `REDIS_URL` | — | work Redis plus current `evt:*` publication for router realtime |
| `REDIS_PREFIX` | `gw` | key/channel namespace, so multiple stacks can share one Redis |
| `WHATSAPP_ADMIN_NUMBER` | — | admin number to provision/pair on boot |
| `GATEWAY_ADMIN_USER_ID` | — | better-auth user id that owns the admin session (optional) |
| `WHATSAPP_ADMIN_CMD_PREFIX` | `am` | private command prefix on admin session |
| `DEFAULT_RATE_PER_MIN` / `DEFAULT_RATE_PER_HOUR` | `20` / `200` | per-session send limits |
| `DEFAULT_AUTO_READ` | `true` | mark inbound read before replying |
| `IGNORE_STATUS` / `IGNORE_GROUPS` / `IGNORE_CHANNELS` / `IGNORE_BROADCAST` | `false` | source filtering |
| `WEBHOOK_*` | — | global webhook defaults |
| `RETENTION_DAYS` | `0` | prune old data |
| `LOG_LEVEL` | `info` | structured logging |

> **Removed vs v1:** `ADMIN_EMAIL`/`ADMIN_PASSWORD`, `USER_PANEL_ENABLED`,
> `WHATSMEOW_STORE_DRIVER` (always SQLite now).

### Frontend (better-auth + TanStack Start)
| Var | Purpose |
|---|---|
| `BETTER_AUTH_SECRET` | better-auth signing/encryption secret |
| `BETTER_AUTH_URL` | frontend's own base URL (issuer/audience) |
| `DATABASE_URL` | MySQL DSN for the frontend's **Drizzle** client — better-auth tables + read-only WA-data queries |
| `GATEWAY_URL` | server-side router base URL (legacy name; points to the router) |
| `VITE_GATEWAY_URL` | browser-facing router base URL (legacy name; never a private gateway URL) |
| `PUBSUB_REDIS_URL` | control bus the frontend publishes `ctrl:*` revocations to; router subscribes |
| `USER_REGISTRATION_ENABLED` | gate self-registration |

---

## 15. Packaging

Two images, deployed independently.

### Gateway Dockerfile (Go, pure-Go SQLite, persistent keystore)
```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/gateway ./cmd/server   # pure-Go sqlite (modernc) → no CGO

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/gateway /usr/local/bin/gateway
VOLUME ["/data/keystore"]                                  # SQLite keystore persists here
EXPOSE 8080
ENTRYPOINT ["gateway"]
```

### Frontend Dockerfile (TanStack Start, Node)
```dockerfile
FROM node:22-alpine AS build
WORKDIR /web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build                                              # TanStack Start server build
FROM node:22-alpine
WORKDIR /web
COPY --from=build /web/.output ./.output
EXPOSE 3000
CMD ["node", ".output/server/index.mjs"]
```

### compose (local, DB included)
```yaml
services:
  router:
    build: { context: ., dockerfile: deploy/Dockerfile.router }
    ports: ["8090:8090"]
    environment:
      ROUTER_HTTP_ADDR: ":8090"
      ROUTER_PUBLIC_URL: "http://localhost:8090"
      MYSQL_DSN: "gw:gwpass@tcp(mysql:3306)/gateway?parseTime=true&charset=utf8mb4"
      REDIS_URL: "redis://redis:6379"
      PUBSUB_REDIS_URL: "redis://redis:6379"
      FRONTEND_ORIGINS: "http://localhost:3000"
      BETTER_AUTH_URL: "http://frontend:3000"
      ROUTER_ED25519_PRIVATE_KEY: "${ROUTER_ED25519_PRIVATE_KEY}"
    depends_on: [mysql, redis]
  gateway:
    build: { context: ., dockerfile: deploy/Dockerfile }
    ports: ["8080:8080"]
    environment:
      MYSQL_DSN: "gw:gwpass@tcp(mysql:3306)/gateway?parseTime=true&charset=utf8mb4"
      WHATSMEOW_STORE_DSN: "file:/data/keystore/store.db?_pragma=foreign_keys(on)&_pragma=journal_mode(WAL)"
      REDIS_URL: "redis://redis:6379"          # work + (default) control bus — single instance
      PUBLIC_URL: "http://gateway:8080"        # private router target
      ROUTER_JWKS_URL: "http://router:8090/.well-known/router-jwks.json"
      APP_ENCRYPTION_KEY: "${APP_ENCRYPTION_KEY}"
      WHATSAPP_ADMIN_NUMBER: "${WHATSAPP_ADMIN_NUMBER}"
    volumes: ["keystore_data:/data/keystore"]               # <-- keystore persistence
    depends_on: [mysql, redis]
  frontend:
    build: { context: ., dockerfile: deploy/Dockerfile.web }
    ports: ["3000:3000"]
    environment:
      DATABASE_URL: "mysql://gw:gwpass@mysql:3306/gateway"
      BETTER_AUTH_URL: "http://localhost:3000"
      BETTER_AUTH_SECRET: "${BETTER_AUTH_SECRET}"
      GATEWAY_URL: "http://router:8090"        # legacy name, router target
      VITE_GATEWAY_URL: "http://localhost:8090"
      PUBSUB_REDIS_URL: "redis://redis:6379"   # publish ctrl:* revocations (same Redis in dev)
    depends_on: [mysql, redis]
  mysql:
    image: mysql:8.4
    environment: { MYSQL_DATABASE: gateway, MYSQL_USER: gw, MYSQL_PASSWORD: gwpass, MYSQL_ROOT_PASSWORD: rootpass }
    command: ["--character-set-server=utf8mb4","--collation-server=utf8mb4_unicode_ci"]
    volumes: ["mysql_data:/var/lib/mysql"]
  redis:
    image: redis:7-alpine
    volumes: ["redis_data:/data"]
volumes: { mysql_data: {}, redis_data: {}, keystore_data: {} }
```

> **Prod / split hosting:** the frontend can run anywhere that reaches MySQL and the public router.
> The gateway runs privately near WhatsApp with its keystore volume; only the router reaches its
> transitional HTTP address. gRPC/mTLS replaces that private seam during this migration.

---

## 16. Repo layout (monorepo)

```
.
├── cmd/router/main.go              # public front door: auth, REST broker, OpenAPI, WebSocket
├── cmd/server/main.go              # gateway entrypoint
├── internal/                       # shared Go packages during router→API migration
│   ├── config/
│   ├── router/                     # public broker + WebSocket; transitional private HTTP proxy
│   ├── authz/                      # router JWT/JWKS + api-key verification
│   ├── assertion/                  # transitional router→gateway Ed25519 trust
│   ├── http/  (gateway private handlers; NO static SPA embed)
│   ├── wa/    (manager, session, store/sqlite, inbound, outbound, events)
│   ├── store/ (MySQL repos — organization_id keyed)
│   ├── webhooks/  · stream/  · queue/
├── migrations/                     # golang-migrate (WA app-data schema only; no wmstore_* in MySQL)
├── web/                            # FRONTEND — TanStack Start + better-auth + copied shadcn
│   ├── app/  (routes, components/ui copied from v1, lib/api, lib/events, lib/auth → better-auth)
│   ├── src/server/auth.ts          # better-auth config (plugins + drizzleAdapter)
│   ├── src/server/db.ts            # Drizzle client (mysql2)
│   ├── src/server/schema/          # Drizzle: auth tables (generated) + WA read-models (introspected)
│   └── drizzle.config.ts           # drizzle-kit config
├── deploy/                         # Dockerfile (gateway) · Dockerfile.web · compose files · .env.example
├── docs/  (openapi.yaml · specs/*.md · mvp-progress.md)
├── .air.toml · Makefile · README.md
```

**Removed from the gateway:** `internal/auth/*` (Authula), public JWT/API-key verification/CORS,
the embedded SPA, gateway NDJSON, OpenAPI serving, the custom MySQL whatsmeow store, and the custom
`api_keys` repo. The router owns current public auth/REST/OpenAPI/WebSocket surfaces.

---

## 17. Migration plan (from v1)

> **Historical v1→v2 sequence.** R1–R5 below record what landed at those points; later central-router
> work superseded direct browser→gateway, per-gateway public auth/CORS/cache, gateway NDJSON, and
> hand-maintained OpenAPI. Current truth is §2–§4, §11–§14.

The v1 code (tagged `mvp-v1`) is functionally complete; v2 is a **re-wiring**, not a rewrite
of the WhatsApp engine. Milestones, each leaving the tree green:

- **R0 — Snapshot & v2 specs (this change):** tag the v1 tree `mvp-v1` (its only home); rewrite this
  masterplan to v2. **Refreshing `docs/specs/*` for v2 is a tracked requirement, not optional**
  — they currently describe removed v1 behavior (Authula, MySQL keystore, `tenant_id`, the SPA).
  First step (now): every stale spec gets a *superseded* banner pointing at the masterplan + its
  owning R-milestone, and `docs/specs/_V2-STATUS.md` maps each spec's disposition. Full per-spec
  rewrites land **with** the R-milestone that re-implements that subsystem (`auth-tenancy.md` →
  `trust-model.md` in R1, `whatsmeow-store.md` → SQLite in R2, `frontend.md` → TanStack Start in
  R3, …) — no spec may describe removed v1 behavior unbannered.
- **R1 — Gateway de-auth:** rip out `internal/auth` (Authula). Add `internal/authz`: JWKS
  fetch+cache, JWT verify (`jwx`) reading `activeOrganizationId`+role from claims, api-key
  verify against `apikey` table (org-scoped). Authorize per-resource by org + role. **Rewrite
  `migrations/` from scratch** for the v2 org-owned schema (pre-release DB reset — drop v1
  `0001_init`/`0002_wmstore`, no backfill). Remove `/auth/*` and `/keys*` routes. Add CORS. Add
  the per-gateway api-key cache + `ctrl:*` control-bus subscriber (`PUBSUB_REDIS_URL`, defaults
  to `REDIS_URL`) handling `apikey.revoked`/`user.banned`/`member.removed`, plus boot key/
  deny-list reconciliation. Gateway boots & verifies a hand-minted JWT.
- **R2 — Keystore → SQLite:** replace the MySQL whatsmeow store with `sqlstore` on
  `modernc.org/sqlite`; add the persistent volume; populate `gateways` (self-row) +
  `wa_sessions.gateway_id`; boot **orphan-guard** (skip + `STOPPED` any session whose owning org
  is gone); re-pair the admin number against SQLite.
- **R3 — Frontend scaffold:** new TanStack Start app; better-auth (email/password, twoFactor,
  admin, apiKey, jwt, **organization**) on MySQL via the **Drizzle adapter** (`@better-auth/cli
  generate` → Drizzle schema, `drizzle-kit migrate` to apply; introspect the gateway-owned WA
  tables into read-only Drizzle models); `definePayload`
  to put `activeOrganizationId`+role in the JWT; **personal-org-on-signup** hook; copy shadcn
  `components/ui` over. **Re-fit the v1 SPA logic to TanStack Start idioms** (route loaders +
  `createServerFn` for data, `createMiddleware`/`beforeLoad` for auth, file-based routing) rather
  than lifting it; port login/register/TOTP, admin user mgmt, key management; add the **org
  switcher**. (Member/invitation UI is the R6 fast-follow.)
- **R4 — Frontend ↔ gateway:** repoint the API client + NDJSON consumer at `GATEWAY_URL`;
  server-side JWT minting + action proxying; hybrid direct-MySQL reads for dashboards/viewer/
  contacts; webhook config via gateway API. Publish `ctrl:apikey.revoked`/`ctrl:user.banned`
  to the control bus on key revoke / user ban (better-auth `after` hook).
- **R5 — Packaging & docs:** two Dockerfiles, split compose, updated `.env.example`,
  `openapi.yaml` (drop auth/keys paths), README; contract test (better-auth key ↔ gateway
  verify); e2e smoke (login → mint JWT → start session → pair → send → stream).
- **R6 — Collaboration (fast-follow):** members & invitations UI on the org plugin (invite by
  email, accept/reject, role changes, remove member); publish `ctrl:member.removed` on removal.
  Ownership + org plumbing already shipped in R1/R3, so this is purely additive.

**Contract tests remain the safety net** for better-auth token/key shape, now consumed by the
router. Private router→gateway assertion tests cover the transitional HTTP seam; gRPC/mTLS contract
tests replace them slice-by-slice during the control-plane migration.

---

## 18. Deferred (post-v2)

Multiple gateways (session sharding across gateways; the registry + `gateway_id` seams exist).
Media subsystem (download/upload, storage, thumbnails) — schema reserves `has_media`/
`media_meta`, media sends `501`. WhatsApp-as-login (`amlogin`) — interceptor + no-op registry
ship now. **Org teams** (sub-groups within an org via the plugin's `teams` option) and
**dynamic org roles** if owner/admin/member proves too coarse. Extra auth plugins
(magic-link / email-OTP, passkeys, captcha + HIBP) — not in the minimal v2 set. Per-session
proxy. Business labels.

> *(The **organization** plugin + org ownership ship in v2 at R1/R3; the members/invitations
> **UI** is the R6 fast-follow — not deferred indefinitely.)*

---

## 19. Open micro-decisions (defaults assumed)

1. **API-key verification mechanism** — replicate better-auth's deterministic key hash and
   look up the shared `apikey` table directly (default; pin the better-auth version + contract
   test) **vs** call `/api/auth/api-key/verify` with a short-TTL cache (fallback if the hash
   isn't replicable). *Confirm against the actual better-auth version in R1/R3.*
2. **JWT delivery to the browser** — *superseded decision:* the browser presents its short-lived
   JWT to the **router** for REST and ticket minting; WebSocket redemption uses the resulting
   single-use ticket. Private gateway addresses and public credentials never meet.
3. **Admin session ownership** — owned by a configured `GATEWAY_ADMIN_USER_ID` **vs**
   system-owned (`user_id` sentinel). Default: configured super-admin user id; falls back to
   system-owned if unset.
4. **History sync on pair** — default **off** (`INGEST_HISTORY` toggle).
5. **Migrations tooling** — *decided:* `golang-migrate` for the **gateway** WA-data plane;
   **drizzle-kit** (`generate`→`migrate`) for the **auth** plane (Drizzle schema produced by
   `npx @better-auth/cli generate`). The better-auth `migrate` CLI (Kysely-only) is **not**
   used. The frontend's WA-table Drizzle models are read-only mirrors — keep in sync via
   `drizzle-kit introspect`.
6. **API-key revocation** — *decided:* frontend publishes Redis `ctrl:*`; the router evicts its
   cache and drops WebSockets, with a ~60-second TTL backstop (§4.6).
7. **Ownership & collaboration** — *decided:* resources owned by **`organization_id`** with a
   **personal org per user** (auto-created on signup); org roles owner/admin/member gate access;
   collaboration via better-auth invitations. Org plumbing ships in v2 (R1/R3); the
   invite/members UI is R6. Plugin set kept **minimal** (email/password, twoFactor, admin,
   apiKey, jwt, organization) — magic-link / passkey / captcha deferred.

---

## 20. Engineering conventions

Carried over from v1, plus the split:

- **Go services:** idiomatic, small focused packages; interfaces only at real boundaries
  (store, WA client, event sink, token/key verifier, gateway engine) defined by the consumer; constructor
  injection; `context.Context` first; errors wrapped with `%w`; `log/slog`; table-driven
  tests; `golangci-lint`; no ORM (plain `database/sql`).
- **Frontend:** TanStack Start; typed API client generated from Huma `openapi.yaml`; TanStack Query
  + router WebSocket for realtime; better-auth (on the **Drizzle** adapter) for identity;
  **Drizzle** as the DB layer (generated auth tables + read-only introspected WA models); shadcn
  primitives; colocate components with routes; server functions for direct MySQL reads (Drizzle)
  and router-mediated actions.
- **Testing (MANDATORY):** every subsystem ships tests in the **same change**. Gateway: pure
  logic table-driven; repos vs SQLite/MySQL test container; handlers via `httptest`; the
  **trust seam** covered by contract tests (§17). External boundaries (whatsmeow, better-auth,
  Redis) faked behind consumer interfaces. `go test ./...` and `pnpm test` are required gates.
- **HTTP:** thin handlers (validate → service → encode); shared request/response/error
  helpers; services hold logic; repos hold SQL.
- **Documentation (`docs/specs/*.md`):** one living spec per subsystem, updated **in the same
  change** as the code. This masterplan is the overview; specs are the detail; `openapi.yaml`
  is the API contract of record.
- **Commits:** Conventional-Commits prefixes; commit **often** in small, green increments;
  touch the relevant `docs/specs/*.md` when behavior changes.

---

## 21. Local development

Fast inner loop: **infra in Docker, both apps on the host.**

- `make infra-up` — MySQL + Redis (ports bound to localhost).
- **Gateway** runs under `air` (hot reload, `CGO_ENABLED=0`, SQLite keystore at a local path).
- **Frontend** runs under the TanStack Start dev server (HMR); better-auth migrations applied
  via its CLI; legacy-named `GATEWAY_URL`/`VITE_GATEWAY_URL` point at the local router.
- The browser hits the frontend; the frontend reads MySQL directly; the browser calls the
  **router** for actions and ticketed WebSocket realtime; the router brokers REST to the gateway.
  CORS allows `http://localhost:3000`.

**Host prerequisites:** Go 1.26+ (toolchain auto-switch per `go.mod`), Node 22+ with pnpm
(`corepack enable`), `air`, `golangci-lint`, Docker (infra). No C compiler needed (pure-Go
SQLite).

**`deploy/docker-compose.dev.yml`** — infra only (MySQL + Redis), unchanged from v1 in shape.

**`.air.toml`** — `CGO_ENABLED=0 go build -o ./tmp/gateway ./cmd/server`; exclude
`web`/`deploy`/`docs`.

**Day-to-day:** `make infra-up` once, then three terminals — gateway (`air`), frontend
(`pnpm dev`), and the better-auth migrate/generate as needed. `make infra-reset` for a clean
DB.
