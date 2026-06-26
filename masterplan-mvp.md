# whatsmeow Gateway — Implementation Spec (v2)

A self-hostable WhatsApp platform split into two independently deployable parts:

- **Gateway** — a focused **Go** service that talks WhatsApp over **whatsmeow** and nothing
  else: pairing, inbound/outbound pipelines, webhooks, the realtime event stream. It has **no
  human login** — it *verifies* callers, it never authenticates them from scratch.
- **Frontend** — a fullstack **TanStack Start** app that owns **all human identity and the
  admin/user surfaces** via **better-auth** (on MySQL), plus the dashboard, viewer, and
  contacts UI. It can be hosted separately from the gateway.

The two share one MySQL database for application data and trust each other through
**better-auth-issued JWTs verified against a JWKS** (humans) and **better-auth API keys**
(machines).

> **Legal / risk notice (ship in README + dashboard footer):** This uses an unofficial
> WhatsApp client. WhatsApp prohibits bots/unofficial clients; automated use may violate its
> Terms and get numbers **banned**. Built-in rate limiting and human-mimicry reduce but don't
> remove the risk. Use at your own risk.

> **History:** This supersedes the v1 single-binary design (Go + embedded Authula + embedded
> React Router SPA). The v1 spec and progress tracker are archived under
> [`docs/archive/`](docs/archive/README.md) and tagged `mvp-v1` in git.

---

## 0. Contents

1. Goals & non-goals · 2. Architecture · 3. Components & responsibilities · 4. Trust & auth
model (JWKS + API keys) · 5. Session model · 6. Storage planes & data ownership · 7. Data
model (DDL) · 8. WhatsApp lifecycle, pairing & admin number · 9. Inbound pipeline · 10.
Outbound pipeline · 11. Eventing & event schema · 12. Frontend (TanStack Start + better-auth)
· 13. Gateway REST API · 14. Configuration · 15. Packaging · 16. Repo layout · 17. Migration
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
- Two delivery mechanisms: an **HTTP chunked NDJSON stream** (for consumers without a public
  URL) and **webhooks** (HMAC-signed, retried).
- REST send API; **two caller identities** — browser users (JWT) and programmatic clients
  (better-auth API keys) — accepted by the same gateway endpoints.
- **MySQL** for messages + a rich identity/contacts model **and** better-auth's tables; both
  the frontend and the gateway read it. **SQLite** (gateway-local, persistent volume) for the
  whatsmeow keystore.
- Read-only WhatsApp viewer; realtime-ish dashboard fed by the gateway stream.
- Docker: gateway and frontend ship as separate images; compose wires them with shared MySQL
  + Redis.

**Designed-for-later (not built in v2, but the seams exist):** **multiple gateways** (a
session is pinned to the gateway that holds its keystore — schema carries `gateway_id` + a
`gateways` registry so sharding is additive). WhatsApp-as-login (`amlogin`).

**Non-goals (v2):** media download/upload (inbound media = metadata only; media sends →
`501`); per-session proxy; Business labels; horizontal scaling of a *single* session.

---

## 2. Architecture

Two services, one shared database, one keystore that lives with the gateway.

```
                            ┌──────────────────────────────────────────────┐
   Browser (admin/user) ───►│  FRONTEND  (TanStack Start, fullstack)        │
                            │  • better-auth: login, register, TOTP, admin  │
                            │    (user CRUD/ban/impersonate/roles), API keys│
                            │  • JWT plugin → JWKS at /api/auth/jwks         │
                            │  • dashboard / viewer / contacts UI            │
                            └───────┬───────────────────────┬───────────────┘
                                    │ reads (display)        │ mints short-lived JWT,
                                    │ direct SQL             │ proxies actions
                                    ▼                        ▼
   programmatic client ───────────────────────────►  ┌───────────────────────────┐
   (Bearer api-key)  ─────────────────────────────►  │  GATEWAY  (Go, whatsmeow)  │
   browser (Bearer JWT, for stream/actions) ──────►  │  • verifies JWT via JWKS   │◄─ws─► WhatsApp
                                                      │  • verifies api-key vs DB  │
                                                      │  • inbound/outbound pipes  │
                                                      │  • webhooks + NDJSON stream│
                                                      └───────┬───────────────┬────┘
                                                              │               │
                          ┌───────────────────────────┐      │ app data      │ keystore
                          │  MySQL (shared)            │◄─────┘ (rw: gateway) │
                          │  • better-auth tables      │◄── reads (frontend)  ▼
                          │    (rw: frontend)          │            ┌──────────────────┐
                          │  • WA-domain tables        │            │ SQLite (gateway- │
                          │    (rw: gateway)           │            │ local, volume):  │
                          └───────────────────────────┘            │ whatsmeow keystore│
                                    ▲                               └──────────────────┘
                                    │ queue / ratelimit / pubsub fan-out / idempotency
                          ┌─────────┴─────────┐
                          │  Redis (gateway)  │
                          └───────────────────┘
```

- **Gateway HTTP framework:** `go-chi/chi` (unchanged from v1).
- **Frontend:** TanStack Start (Vite + TanStack Router + server functions / API routes),
  shadcn components copied over from the v1 SPA, TanStack Query for data, better-auth for
  identity.
- **Trust direction:** the frontend is the *issuer*; the gateway is a *verifier*. The gateway
  never calls the frontend on the request hot path (it caches the JWKS and reads the shared DB
  directly).

---

## 3. Components & responsibilities

| Concern | Owner | Notes |
|---|---|---|
| Login / register / password / TOTP | **Frontend** (better-auth) | replaces all of Authula |
| Roles (`super_admin`/`user`), ban, impersonation, user CRUD, session revoke | **Frontend** (better-auth **admin** plugin) | `/api/auth/admin/*` |
| JWT issuance + JWKS | **Frontend** (better-auth **jwt** plugin) | `/api/auth/token`, `/api/auth/jwks` |
| API keys (create/list/revoke, permissions, expiry, rate-limit; **org-scoped**) | **Frontend** (better-auth **api-key** plugin) | gateway *verifies* them |
| Organizations, members, invitations (co-manage a connection) | **Frontend** (better-auth **organization** plugin) | `/api/auth/organization/*` |
| Admin/user dashboard, viewer, contacts UI | **Frontend** | reads MySQL via Drizzle for display, calls gateway for actions |
| WhatsApp clients (whatsmeow), pairing, reconnect | **Gateway** | one client per number, in-process |
| Inbound normalize/capture/persist/fan-out | **Gateway** | writes WA-domain tables |
| Outbound send, idempotency, rate limit, outbox | **Gateway** | Redis-backed async |
| Webhooks (dispatch, HMAC, retries, dead-letter) | **Gateway** | config surfaced in FE, mutated via gateway API |
| NDJSON event stream | **Gateway** | JWT *or* api-key auth |
| whatsmeow keystore | **Gateway** | SQLite, local persistent volume |
| Sole **writer** of WA-domain tables | **Gateway** | frontend reads them, never writes |
| Sole **writer** of better-auth tables | **Frontend** | gateway reads `apikey` to verify keys; trusts JWT claims for user/org/role |

**Single-writer rule.** Each domain has exactly one writer. The frontend writes auth tables;
the gateway writes WA-domain tables. Cross-reads are allowed (the *hybrid* read pattern, §6),
cross-writes are not. This keeps caches coherent and avoids two services racing on the same
rows.

---

## 4. Trust & auth model (JWKS + API keys)

This is the heart of the v2 design and the answer to "how does the gateway know a request is
legitimate?". There are exactly **two** caller identities, both verified by the gateway with
**no per-request callback to the frontend**.

### 4.1 Humans — better-auth JWT verified via JWKS

1. The frontend runs the better-auth **jwt** plugin. It exposes a **JWKS** at
   `GET {FRONTEND_URL}/api/auth/jwks` and mints short-lived (**5 min**, configurable) **asymmetric** JWTs
   (**EdDSA/Ed25519** by default; ES256/RS256 available) at `GET /api/auth/token`. The private
   key sits in better-auth's `jwks` table (encrypted at rest); the gateway only ever sees
   public keys.
2. On boot (and on a refresh interval / on unknown `kid`), the gateway fetches and **caches**
   the JWKS. It verifies every incoming JWT **locally** with a Go JOSE library
   (`github.com/lestrrat-go/jwx/v2`): signature against the matching `kid`, plus `iss`/`aud`
   == `BETTER_AUTH_URL`, plus expiry.
3. The token payload is customized (`definePayload`) to carry `sub` (user id),
   `activeOrganizationId`, and the member's **org role** (owner/admin/member) — plus the
   platform `role` (for `super_admin`). After verification the gateway has identity, the active
   org, **and** RBAC with zero shared secrets and zero round-trips on the hot path. (better-auth
   doesn't auto-include the active org in the token, so `definePayload` adds it explicitly.)

```
Authorization: Bearer <better-auth JWT>      # browser / dashboard
```

**Where the browser gets the JWT:** the TanStack Start server (which holds the better-auth
session cookie) requests a token from better-auth and hands it to the client, or proxies the
gateway call server-side and attaches it. Because the v1 NDJSON consumer already uses
`fetch` + `ReadableStream` (not `EventSource`), it can attach a `Bearer` header — so the
realtime stream authenticates the same way.

### 4.2 Machines — better-auth API keys verified against the shared DB

Programmatic clients authenticate with a better-auth **api-key** plugin key:

```
Authorization: Bearer <api-key>     # or  x-api-key: <api-key>
```

The frontend's UI creates/lists/revokes keys (with permissions, expiry, rate limits). The
gateway **validates locally against the shared `apikey` table** — consistent with the hybrid
read model — by hashing the presented key with better-auth's scheme and looking up the row,
then checking `enabled` / `expiresAt` / `permissions`. Keys are **org-scoped**
(`organizationId`): a key acts within exactly one organization, and the gateway resolves the
owning org from the key row (the api-key path needs no JWT). This keeps the gateway
self-sufficient (no dependency on the frontend being up). Validated keys are **cached** per
gateway and **revoked instantly** via a shared Redis control bus — see §4.6.

> **Risk to confirm at implementation (§19):** better-auth's api-key hashing must be
> replicable in Go (it hashes keys deterministically by default). **Pin the better-auth
> version** and add a contract test that creates a key in better-auth and validates it in the
> gateway. **Fallback** if the hash proves non-replicable/version-fragile: call the supported
> `POST {FRONTEND_URL}/api/auth/api-key/verify` with a short-TTL in-gateway cache.

### 4.3 Gateway auth middleware

One middleware, two acceptors, evaluated in order:

1. `Authorization: Bearer` that parses as a JWT → verify via JWKS → `{user_id,
   organization_id (active), org_role, platform role}`.
2. Otherwise treat the bearer / `x-api-key` as an **api-key** → verify vs `apikey` table →
   `{organization_id, key permissions}` (no user).
3. Neither → `401`.

The resolved principal is put on the request context; handlers authorize **per-resource by
`organization_id`** — a caller sees only resources owned by their active org. Within the org,
the **member role** (owner/admin manage; member read/send — tunable via the org plugin's
access control) and **api-key permissions** `{read,send,manage,events}` gate the action. A
platform `super_admin` (better-auth admin role, carried in the JWT) can cross orgs for
oversight.

### 4.4 CORS & deployment

Because the frontend and gateway can be on different origins and the browser calls the gateway
directly (stream + actions), the gateway sets **CORS** to allow `FRONTEND_ORIGIN(S)` with
`Authorization` and credentials as needed. Server-to-server (webhooks out, programmatic in)
is origin-agnostic.

### 4.5 Multi-gateway (forward-compatible, not built)

JWT verification and api-key validation are stateless and work for **any number of gateways**
(each verifies independently against the same JWKS + MySQL). The only thing that *doesn't*
fan out is the keystore: a WhatsApp session lives in exactly one gateway's SQLite, so a
session is **pinned** to a gateway. The schema therefore carries `gateway_id` on `wa_sessions`
and a `gateways` registry (one self-row for now). Sharding sessions across gateways is then a
routing change in the frontend, not a redesign.

### 4.6 API-key cache & instant revocation

Each gateway keeps a small **positive cache** of validated keys, so a busy client isn't a
MySQL lookup per request and a brief DB blip doesn't drop in-flight callers:

```
cache[ key_hash ] = { keyId, userId, scopes, expiresAt }    # TTL ~60s, fail-closed
```

indexed so it can evict by `keyId` and by `userId`. The short TTL is the **backstop**: even if
a gateway misses a notification, a revoked key stops working within the window (the `apikey`
row is gone, so the next refresh fails closed).

**Instant revocation (chosen)** rides a **cross-service Redis control bus** shared by the
frontend and every gateway:

1. A user revokes key K (or is banned) in the dashboard → the frontend deletes/disables the
   `apikey` row via better-auth (a MySQL write).
2. In the same handler (or a better-auth `after` hook) the frontend publishes:
   ```
   PUBLISH ctrl:apikey.revoked  {"keyId":"…","userId":"…","ts":…}
   ```
3. Every gateway subscribes to `ctrl:*`. On `apikey.revoked` it (a) evicts the cache entry for
   that `keyId`, and (b) **closes any live NDJSON streams** authenticated by it. A reconnect
   re-validates against MySQL (row gone) → `401`.
4. `ctrl:user.banned {userId}` does the same for **all** of a user's keys and live streams at
   once — and can feed a short JWT **deny-list** (TTL = max JWT lifetime) so the user's
   in-flight JWTs are rejected before their 5-min expiry. `ctrl:member.removed
   {userId, organizationId}` is the org-scoped version — when a collaborator is removed from an
   org, the gateway drops their access to *that* org's streams/resources (deny `(userId, orgId)`
   until JWT TTL ages out), without touching their other orgs.

> **Broadcast, not addressed.** Because the control bus fans out to *all* gateways, the
> frontend never needs to track "which gateway holds this user's sessions" for revocation —
> the gateway that has the entry/connection acts, the rest no-op. (The `gateways` registry of
> §4.5 is for *session pinning*, a separate concern.)

**Two Redis roles** — collapsible to one instance, splittable later:

| Role | Env | Carries | Who connects |
|---|---|---|---|
| **Work** | `REDIS_URL` | asynq queue, rate-limit buckets, idempotency, **NDJSON stream fan-out** (intra-gateway), cache | gateways only |
| **Control bus** | `PUBSUB_REDIS_URL` (defaults to `REDIS_URL`) | low-volume `ctrl:*` pub/sub (key/user revocation, bans) | **frontend** (publish) + all gateways (subscribe) |

- **Single instance (dev / single server):** leave `PUBSUB_REDIS_URL` unset → it falls back to
  `REDIS_URL`; one Redis does everything.
- **Split (prod / multi-gateway):** point `PUBSUB_REDIS_URL` at a shared, possibly-managed
  Redis (e.g. Upstash) reachable by the frontend and every gateway; keep the high-volume work
  Redis local to each gateway. The frontend's **only** Redis dependency is publish access to
  the control bus.

**No collisions on a shared instance.** Namespace by **key/channel prefix**, not Redis DB
number (managed Redis like Upstash often disallows `SELECT`/multiple DBs):

- work keys → `gw:…` (per-gateway state under `gw:{GATEWAY_ID}:…`); asynq keeps its `asynq:` prefix.
- stream fan-out channels → `gw:stream:{sessionId}`.
- control-bus channels → `ctrl:apikey.revoked`, `ctrl:user.banned`, `ctrl:member.removed`.
- a `REDIS_PREFIX` env isolates multiple independent stacks on one Redis.

> **Delivery semantics:** Redis pub/sub is fire-and-forget — a gateway that's down when a
> message is published misses it, which is exactly what the 60-s TTL backstop covers. If you
> later need at-least-once (a just-restarted gateway catching up), promote `ctrl:*` from
> pub/sub to a Redis **Stream** with consumer groups; the call sites keep the same shape.

**Boot reconciliation (catch up on anything missed while down).** The in-memory cache is cold
after a restart, so no stale *cached* key survives a reboot. The real catch-up is over
**persistent** authorizations, done as a one-time sweep on startup:

1. Before the Session Manager resumes each WhatsApp session from the keystore, the gateway
   checks the session's **owning org still exists and is enabled** in MySQL and **skips +
   marks `STOPPED`** any whose org was deleted/disabled while it was down (orphan guard).
2. It reconciles any **persisted** deny-list / known-key state against the shared `apikey`
   table — dropping entries whose key is now revoked/expired/disabled.

A slightly longer boot, but it's once per start and closes the window for `ctrl:*` messages
missed during downtime — complementing the live subscriber and the 60-s cache TTL.

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
  (§4.6); gateways add it to a short **JWT deny-list** (TTL = max JWT lifetime, then the entry
  ages out) and drop the user's live streams. So: **session-revoke = automatic refresh block;
  control-bus = optional instant in-flight kill.**
- **Streams vs short JWTs:** a long-lived NDJSON stream is authenticated **at connect**. The
  client refreshes its JWT and reconnects periodically (and on any network blip), resuming
  seamlessly via `since={lastEventId}` (§11) — so a 5-min token TTL never tears a consumer's
  view; it just triggers a transparent reconnect (the client refreshes ~every 5 min).

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
| **Auth** | `user`, `session`, `account`, `verification`, `jwks`, `apikey`, `organization`, `member`, `invitation`, two-factor + admin tables | **MySQL** | Frontend (better-auth) | Frontend (rw); Gateway (reads `apikey` to verify) |
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
start/stop a session, fetch a QR, register a webhook — the frontend calls the **gateway REST
API**. Realtime comes from the **gateway NDJSON stream**. So:

- **Display data** → frontend reads MySQL directly (low latency, no extra hop).
- **Actions / mutations** → frontend → gateway API (gateway is the single writer).
- **Realtime** → frontend → gateway NDJSON stream.

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
> used). They are **not** redefined here — the gateway treats them as read-only external schema. Match
> `organization_id`/`user_id` lengths to better-auth's ids (`VARCHAR(64)` is a safe default).
> The gateway authorizes from **JWT claims** (`activeOrganizationId` + member role) and api-key
> `organizationId` — it does **not** join `member` on the hot path.
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
  base_url      TEXT NULL,                          -- where the frontend reaches this gateway
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

Same envelope on both transports.

**NDJSON HTTP stream (not SSE):** `GET /api/v1/events?session={id}&events=*` —
`application/x-ndjson`, chunked, one JSON/line. `events=*` subscribes to all types (or
comma-list). **Auth by header** — `Authorization: Bearer <jwt>` (dashboard) or `Bearer
<api-key>` / `x-api-key` (programmatic); same endpoint, §4 middleware. Heartbeat
`{"event":"ping",...}` every ~20s. Resume via `since={eventId}` (replays from `event_log`,
then tails). `session` is an optional filter; omit to stream all your sessions.

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
components are copied over** from `web/app/components/ui/*` (Radix-based, framework-agnostic —
they port to TanStack Router with minimal change); the v1 API client, NDJSON consumer, and
event-bus logic are reused, repointed at the gateway origin.

**Porting from the v1 SPA (reshape, don't lift).** v1 is a **client-only SPA** (CSR, embedded
in the Go binary); v2 is **fullstack TanStack Start** (SSR + server functions). Reuse the
*logic*, but re-fit it to TanStack Start's idioms rather than copying the SPA wiring verbatim:

- **Data fetching** → route **loaders** + **server functions** (`createServerFn`), not
  client-only `useEffect`/fetch. Initial/SSR reads run **server-side** (Drizzle direct reads
  §6.2, or a gateway proxy); the client hydrates from loader data and uses TanStack Query for
  mutations + the NDJSON stream for realtime.
- **Auth** → **server middleware** (`createMiddleware`) + route `beforeLoad` that resolve the
  better-auth session, attach `{user, activeOrg, role}`, gate routes, and mint the gateway JWT —
  replacing v1's client-side route guards.
- **Routing** → TanStack Router **file-based** routes (route tree, `beforeLoad`/`loader`/
  context), re-mapping the v1 React Router route objects.
- **Reused ~as-is:** shadcn `components/ui`, the NDJSON parser + event-bus, Zod schemas, and the
  generated API types.

**Identity (better-auth on MySQL via the Drizzle adapter):** mounted at `/api/auth/*`.
`drizzleAdapter(db, { provider: "mysql" })`; the auth-table Drizzle schema is generated by
`npx @better-auth/cli generate`. Plugins:

| Plugin | Role |
|---|---|
| Email & Password (core) | login + registration (gated by `USER_REGISTRATION_ENABLED`) |
| Two-Factor (`twoFactor`) | optional TOTP 2FA |
| Admin (`admin`) | roles `super_admin`/`user`; user CRUD, ban/unban, impersonation, session revoke — `/api/auth/admin/*` |
| API Key (`apiKey`) | programmatic keys with permissions/expiry/rate-limit (gateway verifies) |
| JWT (`jwt`) | short-lived JWTs + JWKS at `/api/auth/jwks`; payload carries `activeOrganizationId` + member role |
| Organization (`organization`) | orgs, members, roles, **email invitations**; a **personal org** auto-created per user on signup |

> Roles: **platform** `super_admin` bootstrapped via the `admin` plugin's `adminUserIds` (or
> the first user on seed) — cross-org oversight. **Org** roles owner/admin/member gate access
> *within* an org. `user` self-registers (toggleable), gets a personal org, attaches numbers to
> an org, manages that org's keys/webhooks, and can invite collaborators. A member sees data for
> the orgs they belong to (active org at a time).

**Server vs client (serverless-friendly — the gateway owns long-lived connections):** The
frontend server does **only short-lived work** — better-auth endpoints, **minting JWTs**
(`/api/auth/token`), and **direct MySQL reads via Drizzle** for SSR/loaders (§6.2). It does **not proxy
gateway traffic**. The **browser talks to the gateway directly** (CORS + `Bearer` JWT) for
**both actions and the NDJSON stream**. This is what lets the frontend run on **serverless**
(Vercel/Cloudflare/Netlify), where a function can't hold a long-lived streaming proxy open:
the stream lives in the gateway, and the browser connects to it directly.

- **Server** (serverless): auth, token mint, direct MySQL reads. No streaming, no proxy.
- **Client:** TanStack Query for data; the **fetch+ReadableStream NDJSON** consumer for
  realtime — both hitting the gateway with a `Bearer` JWT, refreshed per §4.7.
- *(Optional, non-serverless only:)* a long-running frontend host can proxy gateway calls
  server-side to hide the gateway URL / avoid client-side JWTs. Not required, and incompatible
  with serverless for the stream.

> **Serverless + MySQL:** direct reads from serverless functions can exhaust DB connections —
> use a pooler / serverless-friendly path (PlanetScale, RDS Proxy, or a driver with
> connection reuse). The gateway, a long-running process, keeps a normal pool.

**Realtime-ish:** open the gateway NDJSON stream (`GET {GATEWAY_URL}/api/v1/events?events=*`).
Live surfaces: session statuses, admin-number/QR/pairing code, the viewer's incoming messages,
event monitor. Auto-reconnect with `since={lastEventId}`; polling fallback if the stream drops.

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

## 13. Gateway REST API

**Principles:** resource-oriented; **one way to do a thing** (single send endpoint);
consistent response/error/pagination envelopes; **session is always a path param**; plural
nouns + standard verbs; non-CRUD actions as explicit sub-resources (`:start`, `/reaction`).
Base `/api/v1`. **Auth:** every endpoint takes a JWT or api-key (§4). The gateway **no longer
serves `/auth/*` or key management** — those live in the frontend (better-auth). Errors:

```json
{ "error": { "code": "rate_limited", "message": "…", "details": {} } }
```

Lists: `?limit=&cursor=` (opaque cursor over `id`). Spec of record: `docs/openapi.yaml` (no
Swagger UI; raw spec optionally at `/api/v1/openapi.yaml`).

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
| GET | `/events` (NDJSON, §11) |
| POST/GET/PATCH/DELETE | `/webhooks` · `/webhooks/{id}` (config; surfaced in FE) |
| GET | `/admin/sessions` — cross-org WhatsApp oversight (`super_admin`) |
| GET | `/healthz` · `/readyz` · `/metrics` (Prometheus) |

> **Removed vs v1:** `/auth/*` (→ better-auth), `/keys*` (→ better-auth api-key plugin),
> `/auth/admin/*` (→ better-auth admin plugin).

---

## 14. Configuration (ENV)

### Gateway
| Var | Default | Purpose |
|---|---|---|
| `HTTP_ADDR` | `:8080` | listen addr |
| `GATEWAY_ID` | `gw-1` | this gateway's id (rows in `gateways`, `wa_sessions.gateway_id`) |
| `PUBLIC_URL` | — | external base URL of this gateway |
| `BETTER_AUTH_URL` | — | frontend base URL; the JWT `iss`/`aud` to enforce |
| `BETTER_AUTH_JWKS_URL` | `${BETTER_AUTH_URL}/api/auth/jwks` | JWKS to fetch/cache |
| `FRONTEND_ORIGINS` | — | comma-list of allowed CORS origins (browser → gateway) |
| `APP_ENCRYPTION_KEY` | — | base64 32-byte AES-GCM key (webhook secrets at rest) |
| `MYSQL_DSN` | — | shared app-data DSN (WA tables rw; `apikey`/`user` ro) |
| `WHATSMEOW_STORE_DSN` | `file:/data/keystore/store.db?...` | **SQLite** keystore (persistent volume) |
| `REDIS_URL` | — | **work** Redis: asynq queue, rate-limit, idempotency, stream fan-out, cache |
| `PUBSUB_REDIS_URL` | `${REDIS_URL}` | **control bus** — cross-service `ctrl:*` pub/sub (key/user revocation). Defaults to `REDIS_URL` (single instance) |
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
| `GATEWAY_URL` | base URL the frontend calls for actions/stream |
| `PUBSUB_REDIS_URL` | **control bus** the frontend publishes `ctrl:*` revocations to (= the gateways' `PUBSUB_REDIS_URL`; in single-instance dev, the gateway's `REDIS_URL`). Publish-only; the frontend never touches the work Redis |
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
  gateway:
    build: { context: ., dockerfile: deploy/Dockerfile }
    ports: ["8080:8080"]
    environment:
      MYSQL_DSN: "gw:gwpass@tcp(mysql:3306)/gateway?parseTime=true&charset=utf8mb4"
      WHATSMEOW_STORE_DSN: "file:/data/keystore/store.db?_pragma=foreign_keys(on)&_pragma=journal_mode(WAL)"
      REDIS_URL: "redis://redis:6379"          # work + (default) control bus — single instance
      BETTER_AUTH_URL: "http://frontend:3000"
      FRONTEND_ORIGINS: "http://localhost:3000"
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
      GATEWAY_URL: "http://gateway:8080"
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

> **Prod / split hosting:** the frontend can run anywhere (Vercel/Node/container) as long as
> it reaches MySQL and the gateway; the gateway runs near WhatsApp with its keystore volume.
> They need not be co-located — that's the whole point of v2.

---

## 16. Repo layout (monorepo)

```
.
├── cmd/server/main.go              # gateway entrypoint
├── internal/                       # GATEWAY (Go) — internal/auth/* REMOVED
│   ├── config/
│   ├── authz/                      # NEW: JWT/JWKS verify + api-key verify middleware (was internal/auth)
│   ├── http/  (router, middleware, handlers, NO static SPA embed)
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
├── docs/  (openapi.yaml · specs/*.md · archive/ [v1 snapshot])
├── .air.toml · Makefile · README.md
```

**Removed from the gateway:** `internal/auth/*` (Authula), the embedded SPA (`internal/http/
static`), the custom MySQL whatsmeow store (`wa/store/mysql` → `wa/store/sqlite`), the custom
`api_keys` repo. **Added:** `internal/authz` (JWKS + api-key verification).

---

## 17. Migration plan (from v1)

The v1 code (tagged `mvp-v1`) is functionally complete; v2 is a **re-wiring**, not a rewrite
of the WhatsApp engine. Milestones, each leaving the tree green:

- **R0 — Snapshot & v2 specs (this change):** archive v1 docs + tag `mvp-v1`; rewrite this
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

**Contract tests are the safety net** for the trust seam: one that mints a better-auth JWT and
verifies it in the gateway; one that creates a better-auth api-key and validates it in the
gateway. Both must pass in CI.

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
2. **JWT delivery to the browser** — *decided:* **direct browser→gateway** for both actions
   and the stream (browser fetches a short-lived JWT from `/api/auth/token`, calls the gateway
   with `Bearer`; refresh per §4.7). Keeps the frontend **serverless-compatible** (no
   long-lived proxy). Server-side proxying stays available for non-serverless hosts that prefer
   to hide the gateway URL / avoid a client-side JWT.
3. **Admin session ownership** — owned by a configured `GATEWAY_ADMIN_USER_ID` **vs**
   system-owned (`user_id` sentinel). Default: configured super-admin user id; falls back to
   system-owned if unset.
4. **History sync on pair** — default **off** (`INGEST_HISTORY` toggle).
5. **Migrations tooling** — *decided:* `golang-migrate` for the **gateway** WA-data plane;
   **drizzle-kit** (`generate`→`migrate`) for the **auth** plane (Drizzle schema produced by
   `npx @better-auth/cli generate`). The better-auth `migrate` CLI (Kysely-only) is **not**
   used. The frontend's WA-table Drizzle models are read-only mirrors — keep in sync via
   `drizzle-kit introspect`.
6. **API-key revocation** — *decided:* **instant** via the Redis control bus (`ctrl:*`) plus a
   ~60-s cache TTL backstop (§4.6). Pub/sub for v2; promote to a Redis Stream only if
   at-least-once delivery is later required.
7. **Ownership & collaboration** — *decided:* resources owned by **`organization_id`** with a
   **personal org per user** (auto-created on signup); org roles owner/admin/member gate access;
   collaboration via better-auth invitations. Org plumbing ships in v2 (R1/R3); the
   invite/members UI is R6. Plugin set kept **minimal** (email/password, twoFactor, admin,
   apiKey, jwt, organization) — magic-link / passkey / captcha deferred.

---

## 20. Engineering conventions

Carried over from v1, plus the split:

- **Go (gateway):** idiomatic, small focused packages; interfaces only at real boundaries
  (store, wa client, event sink, **token/key verifier**) defined by the consumer; constructor
  injection; `context.Context` first; errors wrapped with `%w`; `log/slog`; table-driven
  tests; `golangci-lint`; no ORM (plain `database/sql`).
- **Frontend:** TanStack Start; typed API client generated from `openapi.yaml`; TanStack Query
  + the NDJSON stream for realtime; better-auth (on the **Drizzle** adapter) for identity;
  **Drizzle** as the DB layer (generated auth tables + read-only introspected WA models); shadcn
  primitives; colocate components with routes; server functions for direct MySQL reads (Drizzle)
  and gateway proxying.
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
  via its CLI; `GATEWAY_URL` points at the local gateway.
- The browser hits the frontend; the frontend reads MySQL directly and calls the gateway for
  actions/stream. CORS allows `http://localhost:3000`.

**Host prerequisites:** Go 1.26+ (toolchain auto-switch per `go.mod`), Node 22+ with pnpm
(`corepack enable`), `air`, `golangci-lint`, Docker (infra). No C compiler needed (pure-Go
SQLite).

**`deploy/docker-compose.dev.yml`** — infra only (MySQL + Redis), unchanged from v1 in shape.

**`.air.toml`** — `CGO_ENABLED=0 go build -o ./tmp/gateway ./cmd/server`; exclude
`web`/`deploy`/`docs`.

**Day-to-day:** `make infra-up` once, then three terminals — gateway (`air`), frontend
(`pnpm dev`), and the better-auth migrate/generate as needed. `make infra-reset` for a clean
DB.
