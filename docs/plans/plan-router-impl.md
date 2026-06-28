# Plan: Central Router + Gateway Accounting (Layer 1)

Status: **proposed** — design of record for the centralization work. No code yet.
Branch: `feat/central-router`.

This document is the implementation plan for two converged changes:

1. **Central Router** — a single front door so callers use *one* base URL + their token + a
   session id, and the platform brokers the call to whichever gateway owns that session. The
   router also becomes the system's **single trust boundary** (authentication + the realtime
   WebSocket endpoint).
2. **Gateway accounting — Layer 1** — make adding/removing gateways a clean, observable,
   data-driven operation via a proper `gateways` registry lifecycle.

Keystore portability (**Layer 2** — making sessions freely re-homable across gateways) is
explicitly **out of scope** here and documented as a future upgrade at the end.

---

## 0. Optimization goal: refactor hard, delete more, checkpoint often

This is a **pre-release, no-backward-compat** codebase (`CLAUDE.md`: "reshape the schema freely",
v1 archived at tag `mvp-v1`). So this plan optimizes along **three axes — less config, less code,
more modular** — not minimal diffs. Explicit priorities:

1. **Modular by default.** Prefer **one reusable module parameterized by config** over forked,
   per-consumer copies; clean package seams; swappable implementations behind consumer-defined
   interfaces (the codebase's existing pattern — `CLAUDE.md`). Examples in this plan: the
   `JWTVerifier` module used by *both* the router (better-auth JWKS) and the gateway (router JWKS),
   differing only by URL (D3); the transport-agnostic stream core + pluggable WS adapter (D5); the
   `EventSink` with direct-push / Redis implementations (D6); huma route registrars as pure,
   composable functions (D11).
2. **Less config beats less code when they conflict.** Fewer knobs to set/distribute/rotate is worth
   keeping a small, *shared* module. (Why D3 repoints the JWKS verifier instead of shipping a static
   key: one URL + automatic rotation, no key distribution.)
3. **Delete > move > add.** When a responsibility shifts (e.g. authn → router), prefer outcomes that
   let a large surface be *removed* from the repo, not just relocated. Where code must move, leave no
   parallel copy behind. (Subordinate to 1–2: don't fork a module just to delete lines.)
4. **Collapse boilerplate into the framework.** huma (D11) is justified partly because it *deletes*
   repetitive decode/validate/error-mapping in every handler. Favor less hand-rolled plumbing.
5. **No "back-compat" shims.** Nothing ships yet: **WS-only** (delete NDJSON), **generated spec only**
   (delete the hand-written yaml), **one authn implementation** (router). No keep-the-old-path hedges.
6. **Green at every checkpoint.** Each commit builds + passes both halves' gates (§ Green gates,
   `CLAUDE.md`). Refactors land as `add replacement → switch callers → delete old`, three green
   commits, so each deletion is isolated and reviewable. See §11.

**Tie-break rule (record it once):** when the axes conflict, prefer **modular + less-config** over
raw line deletion. A shared module that costs lines but removes a knob and serves two consumers wins
over a bespoke path that saves lines but forks behavior or adds config.

The concrete deletion inventory is §10; the checkpoint strategy is §11.

---

## 1. Why

Today (per the v2 specs) the **browser/API client talks to a gateway directly**:

- `trust-model.md`: *"the browser then calls the gateway directly with `Bearer`."*
- `stream.md`: *"the browser connects to the gateway directly."*
- Each gateway authenticates end-user identity **itself**, locally (JWKS verify + better-auth
  api-key hash), and serves its own NDJSON event stream.

This was correct when the gateway was the only reachable service. It creates three problems as
soon as there is (or might be) more than one gateway:

- **Host discovery is the caller's problem.** A session is pinned to a `gateway_id`
  (`whatsmeow-store.md`), so the client must know *which gateway host* holds its session. That is a
  headache we should not export to users.
- **The trust seam is duplicated per gateway.** Every gateway re-implements and re-pins
  better-auth's api-key hash + JWKS verification (`internal/authz`), and runs the control-bus
  subscriber. N gateways = N copies of the most security-sensitive, version-pinned code.
- **Realtime is per-gateway.** The event stream lives on each gateway, so a client must connect to
  the right one — and we want to move to a single WebSocket endpoint anyway.

The router fixes all three: one base URL, one trust boundary, one realtime endpoint. The gateway
becomes a lean internal WhatsApp engine.

### What the existing design already got right (so this is evolution, not a rewrite)

- `wa_sessions.gateway_id` + the `gateways` registry — the routing table the router reads
  (`store.md`, `whatsmeow-store.md`; called "forward-compat for §4.5 multi-gateway routing").
- `gateways.base_url` (proxy target) and `gateways.last_seen_at` (heartbeat) columns already exist.
- Org-keyed ownership everywhere (`organization_id`) — clean isolation key.
- Control bus is **broadcast, not addressed** — the frontend never tracked which gateway holds a
  session, so a router in front disturbs nothing about revocation.
- `EventSink`/`Publisher` is an interface — Redis-publish vs. direct-push is a swap behind it.
- `internal/authz` already separates **verifiers** (JWT/api-key) from **gates** (capability) — the
  exact seam we split along.

---

## 2. Target architecture

```
                    ┌─────────────────────────────────────────────┐
   browser / API ──▶│  cmd/router  (NEW · stateless · Go)          │
   one base URL,    │  • two-acceptor AUTHN (JWKS JWT + api-key)   │
   bearer + session │  • session→gateway resolve + org isolation   │
                    │  • REST: ReverseProxy to owning gateway      │
                    │  • WS: single realtime endpoint (ticket auth)│
                    │  • subscribes ctrl:* (revocation)            │
                    └───┬───────────────────────────┬─────────────┘
           Ed25519-signed, request-bound     subscribe / receive events
           internal assertion  │                    │
              ┌────────────────┼──────────┐         │
              ▼                ▼           ▼         │
        ┌──────────┐    ┌──────────┐  ┌──────────┐  │
        │ gateway1 │    │ gateway2 │  │ gateway3 │  │   gateways PUBLISH events:
        │ whatsmeow│    │ whatsmeow│  │ whatsmeow│──┘   • direct-push to router (default), or
        │ +SQLite  │    │ +SQLite  │  │ +SQLite  │      • shared Redis evt:* (at HA)
        │ (verify  │    └──────────┘  └──────────┘
        │  router  │
        │  assert) │
        └────┬─────┘
             └──── shared MySQL (wa_sessions, gateways registry, event_log, apikey) ────┘
             └──── one Redis (asynq work queue + ctrl:* bus [+ evt:* if HA]) ───────────┘
```

Properties:

- **Gateways need no public client surface.** They serve only the internal API (called by the
  router) + `/healthz` `/readyz` `/metrics`. Safe even if internet-reachable (see §6 threat model).
- **"Frontend can be down" survives.** The router authenticates JWTs against cached JWKS and
  api-keys against the shared `apikey` **table** — no per-request callback to the frontend, exactly
  the property the gateway had.
- **One Redis is sufficient** (collapsed roles, namespaced); the split to multi-Redis is a later
  env change (`queue.md`).

---

## 3. Design decisions

### D1 — Router is a new entrypoint in *this* Go module, not a new repo / not Node

`cmd/router/` alongside `cmd/server/`. It imports `internal/authz`, `internal/store`,
`internal/controlbus`, `internal/httpx`, `internal/stream` (refactored). **Rationale:** the trust
seam (better-auth api-key hash pinned by contract test, EdDSA JWKS verify) already lives in Go; a
second language would duplicate and re-pin the most security-critical code. Shared `internal/`,
different `main`.

### D2 — Authentication terminates at the router; the gateway trusts the router

The two-acceptor authn (`internal/authz` verifiers) runs **only** in the router. The router
resolves the full `Principal` (org, userId, orgRole, platformRole, keyId, permissions) and forwards
it to the gateway as a **signed internal assertion** (D3). The gateway no longer verifies end-user
JWTs or api-keys.

### D3 — proxy→gateway: Ed25519, request-bound, anti-replay

The router mints a short-lived **Ed25519-signed internal assertion** per proxied request (a compact
JWS via the existing `lestrrat-go/jwx`). The router **publishes its own JWKS**; the gateway verifies
the assertion by **repointing the existing `JWTVerifier` module at `ROUTER_JWKS_URL`**.

> **Modular + less-config choice (per §0 tie-break).** `internal/authz/jwt.go` becomes **one reusable
> module with two consumers**: the *router* verifies better-auth's JWKS, the *gateway* verifies the
> *router's* JWKS — same code, different configured URL. This beats a static `ROUTER_PUBLIC_KEY`:
> one URL instead of distributing a key, **automatic rotation** (router rotates → publishes → gateway
> picks it up, no overlap config), and no forked static-key verify path. The lines it "keeps" are a
> shared module earning double use, not duplication. Cost: the gateway fetches the router's JWKS at
> boot/refresh (cached ~1h; the router is the always-up front door) — an acceptable, well-trodden
> dependency.

Assertion claims (all load-bearing):

| Claim | Purpose |
|---|---|
| `org`, `userId`, `orgRole`, `role`, `keyId`, `permissions` | the resolved principal the gateway gates on |
| `aud` = target `gateway_id` | cannot be replayed against another gateway |
| `session` | bound to the specific session being acted on |
| `method`, `path` | cannot be replayed against a different endpoint |
| `bodyHash` = `base64url(SHA-256(body))` | cannot be reused with a swapped payload |
| `iat`, `exp` (~30s) | tight freshness window |
| `jti` | one-time nonce |

Gateway verification order: signature (router JWKS) → `aud` == own `GATEWAY_ID` → `exp`/`iat` within
skew → `method`/`path`/`bodyHash` match the actual request → **`jti` not already seen** (reject
replay). The router holds the Ed25519 **private** key; the gateway holds only the **public** key, so
a compromised/exposed gateway can never forge an assertion.

**Anti-replay store:** a nonce cache keyed by `jti`, TTL = freshness window. In-memory per gateway
instance is sufficient **unless a single gateway runs multiple replicas behind a load balancer** —
then the nonce store must be shared across those replicas. Single-instance gateways: in-memory.
Requires router/gateway clocks on NTP with small skew tolerance (~5s).

### D4 — authz split: verify at router, gate + scope at gateway

`internal/authz` splits along its existing seam:

| Concern | Where it runs after this change |
|---|---|
| JWKS fetch/cache, JWT verify, api-key hash+lookup, positive cache, JWT deny-list | **router only** |
| Resolve `Principal` | **router only** |
| Capability gates (`RequireRead/Send/Manage/Events/SuperAdmin`, `gates.go`) | **gateway** — reads principal from the verified assertion (D3) |
| Org-scoped queries (`WHERE organization_id = ?`, `internal/store`) | **gateway** — unchanged; uses the asserted org |

The gateway keeps `gates.go` + `context.go` (principal type) and gains a small middleware that
verifies the router assertion and populates the principal from it. It drops the better-auth-specific
verifiers and their cache.

### D5 — Realtime: single WebSocket endpoint on the router

The router is the **only** client-facing realtime endpoint. The `internal/stream` handler's
replay/tail/heartbeat/registry logic is lifted into a **transport-agnostic core** with a single
**WebSocket** adapter. Per §0 (no back-compat), the **NDJSON transport is deleted** — after the
cutover nothing consumes it (the gateway stops serving `/events`, the frontend moves to WS, and
gateway→router events use the direct-push ingest, not the client stream).

- **Connect auth via a Redis-backed, single-use, scope-bearing ticket** (see D5a below). Native
  browser `WebSocket` cannot set an `Authorization` header (the reason the current stream uses
  `fetch`+`ReadableStream`). Authorization happens **once, at mint time**, where the bearer and full
  principal are available; the WS handshake merely **redeems** the ticket.
- **`?since={lastEventId}` durable replay** preserved for org/session scopes — replays from
  `event_log` (shared MySQL) then tails, with boundary dedup (as today in `stream.md`). (Firehose is
  live-tail only — see D5a.)
- **Control-bus drop** preserved — the router holds the live-connection registry keyed by principal
  and drops connections on `ctrl:apikey.revoked` / `ctrl:user.banned` / `ctrl:member.removed`.
- **Seamless 5-min-JWT reconnect** preserved — client refreshes its JWT, mints a fresh ticket,
  reconnects with `since=`.

Gateways stop serving the client `/events` route entirely.

### D5a — Ticket model: pre-authorized, scope-bearing, single-use

The ticket is **not** a bare handshake token — it carries the *resolved, already-authorized*
subscription scope. This is what lets one mechanism serve both a user watching one session and the
**admin firehose** dashboard.

**Mint** — `POST /api/v1/realtime/ticket` (bearer-authenticated, `RequireEvents` for user scopes,
`RequireSuperAdmin` for firehose). Request body declares the *requested* scope:

```jsonc
{
  "scope":   "session" | "organization" | "firehose",
  "session": "wa_sess_…",        // required iff scope=session
  "events":  ["message.received", …] | ["*"],   // default ["*"]
  "since":   "evt_…",            // optional resume cursor (org/session scopes)
  "gateway": "gw_…"             // optional, firehose only: filter to one source gateway
}
```

**Authorization is enforced here, at mint** (the load-bearing check):

| `scope` | Who | Subscribes to | Authz at mint |
|---|---|---|---|
| `session` | user | exact `evt:{org}:{session}` | `events` capability **and** session.org == principal.org |
| `organization` | user | `evt:{org}:*` (all the caller's sessions) | `events` capability; org = principal's org |
| `firehose` | admin | `evt:*` (all orgs, all sessions, all gateways) | `super_admin` (JWT `role`) |

On success the router writes the **resolved** scope to Redis and returns the ticket id:

- Redis key `rt:ticket:{id}` → JSON of the *resolved* params (not the raw request), e.g.:
  ```jsonc
  {
    "scope": "organization",
    "organization": "org_…",       // resolved; null/"*" for firehose
    "session": "wa_sess_…|*",
    "events": ["*"],
    "since": null,
    "gateway": null,
    "principal": { "userId": "…", "keyId": null, "orgRole": "…", "role": "…" }
  }
  ```
- **Short TTL** (~30s) so an unredeemed ticket self-expires.
- Response: `{ "ticket": "rt_…", "expiresInSeconds": 30, "url": "wss://…/api/v1/realtime?ticket=rt_…" }`.

**Redeem** — `GET /api/v1/realtime?ticket=rt_…` (WS upgrade). The router does an **atomic
`GETDEL rt:ticket:{id}`** (Redis 6.2+) — this both reads the scope and **consumes the ticket in one
step**, so it is single-use even across router replicas (a second connect with the same ticket finds
nothing → `4401` WS close). If found, the router:

1. **Registers** the live connection in the registry keyed by the ticket's `principal`
   (`userId`/`keyId`/`org`) so `ctrl:*` can drop it.
2. **Subscribes** per the resolved scope (exact channel / `evt:{org}:*` / `evt:*`), applying the
   `events` type filter per event (peek `id`+`event`, as today) and the optional `gateway` filter
   for firehose.
3. **Replays** from `event_log` if `since` is set and scope is org/session, then tails. **Firehose
   is live-tail only** (no durable replay) — a cross-org `event_log` scan is intentionally avoided;
   document this limit.

**Why Redis-stored rather than a signed self-contained ticket:** single-use requires server-side
state to atomically mark "used → gone." `GETDEL` gives that for free and across replicas; a stateless
signed token would need a used-jti set in Redis anyway — same cost, more complexity. Redis is already
reachable by the router (it is the `ctrl:*` bus). Namespace: `rt:` (realtime).

**Security notes:** the WS handshake performs **no re-authn** — it trusts the redeemed ticket,
which is sound because (a) authz was applied at mint with the full principal, (b) the short TTL +
single-use bound the theft window, and (c) post-connect revocation is still covered by the live
registry + `ctrl:*` drop. The ticket id travels in the URL query (logs/referrer exposure) — mitigated
by TLS + the short TTL + single-use; never put the bearer itself in the URL.

> **Firehose gateway dimension.** Events are published per `(org, session)` regardless of which
> gateway produced them, so "all gateways" falls out of `evt:*` naturally. To *filter by* a specific
> gateway, the event envelope (or the router's direct-push ingest) must tag the **source
> `gateway_id`**; under direct-push the router knows the producer, under Redis the gateway must stamp
> it. Add a `gatewaySource` field to the envelope if firehose gateway-filtering is required (see
> `eventing.md`).

### D6 — Events transport: direct-push default, shared-Redis at HA (behind `EventSink`)

The gateway already emits via the `EventSink`/`Publisher` interface. Two implementations behind it:

- **Default (single router):** gateway **pushes events directly to the router** over an internal
  ingest channel (authenticated by an Ed25519 assertion, same mechanism as D3 in the gateway→router
  direction). No Redis in the event path.
- **At HA (≥2 routers):** gateway **publishes to shared Redis** `evt:{org}:{session}`; every router
  replica subscribes so the event reaches whichever replica holds the client's WS.

The call site does not change — only which `EventSink` is wired. Start with direct-push.

### D7 — One Redis, namespaced; control bus consumer becomes the router

- One Redis instance is sufficient now (`queue.md`: roles are logical, collapsible). Namespacing
  keeps them apart: `asynq:` (work queue), `gw:{GATEWAY_ID}:*` (work keys), `wa:rl:*` (rate-limit),
  `ctrl:*` (revocation), `evt:{org}:{session}` (events, only if HA path), `REDIS_PREFIX` to isolate
  stacks.
- The **control-bus subscriber moves to the router** (it owns the key cache + WS drop). The
  **gateway drops `internal/controlbus` entirely** — its only jobs were authz-cache eviction and
  stream drop, both now the router's.
- The frontend remains **publish-only** to `ctrl:*`; whichever Redis carries `ctrl:*` must be
  reachable by the (serverless) frontend (auth + `rediss://`).
- The split to a dedicated `PUBSUB_REDIS_URL` later is an env change, no code change.

### D8 — Gateway accounting Layer 1: registry lifecycle

Make add/remove clean and observable (mechanics; this does **not** make sessions portable — see
Layer 2). See §5 for the schema and §4.2 for the gateway-side behavior.

- Lifecycle `status`: `joining → active → draining → drained`, plus `unreachable` (derived/marked
  from stale heartbeat).
- Heartbeat loop writes `last_seen_at` (+ `session_count`).
- Self-register on boot; graceful **drain** on `SIGTERM` (stop accepting new placements, finish
  in-flight, mark `drained`).
- **Placement** of new sessions: the router (or the create-session path) picks the least-loaded
  `active` gateway from the registry.
- **Stranded-session handling:** a session pinned to an `unreachable`/removed gateway → router
  returns `503 gateway_unavailable` with a clear message, not a silent hang.

### D9 — OpenAPI stays a shared contract at repo root; the router serves it

The public API contract is no longer "the gateway's." But it should **not** move into `web/` (a
*consumer* that generates the typed client + docs) nor be embedded inside the router (which would
imply the router owns resource schemas it merely proxies, and `go:embed` can't reach `../docs`
anyway). It is **co-authored** — the gateway implements most request/response schemas, the router
adds the front-door (auth framing, `realtime/ticket`, WS) — and **multi-consumed** (router serves,
web generates). So:

- **Canonical file stays at `docs/openapi.yaml`** (repo-root, shared system contract) — but it is
  now a **generated, committed artifact**, not hand-authored (see D11).
- **The router serves it** at `/api/v1/openapi.yaml` via the existing runtime `OpenAPIPath`
  mechanism (mount/copy `docs/openapi.yaml` into the router image). The **gateway stops serving**
  the public spec.
- **Reframe ownership language** (not the file location) in `CLAUDE.md` + `http-foundation.md`:
  it is the *public API contract of record, served by the router* — resource schemas implemented by
  the gateway, front-door/realtime by the router, typed client + docs generated by web.
- The gateway's internal router↔gateway surface is **not** part of this public contract.

### D11 — Code-first: shared Go types are the source of truth; the spec is generated

Flip from spec-first (hand-write `openapi.yaml`) to **code-first** so there is no Go↔spec or
router↔gateway skew. The generation chain: **shared Go types → `docs/openapi.yaml` → TS client
(`pnpm gen:api`) + docs (`pnpm docs:openapi`)**. Every downstream artifact is derived; nothing is
hand-synced.

- **Library: [huma](https://huma.rocks)** (mounts on the existing **chi**). Operations are declared
  with Go input/output structs; huma generates **OpenAPI 3.1** from them *and* validates
  requests/responses against the same structs at runtime — so the spec cannot misdescribe what the
  handler accepts/returns (this is what closes the handler↔type skew that a comment-based generator
  like swaggo leaves open).
- **Shared DTO package** `internal/apitypes` (or per-resource): the **gateway** defines resource
  schemas (sessions, messages, contacts…), the **router** defines front-door/realtime
  (`realtime/ticket`, …). Both import it → router↔gateway types can't diverge.
- **Route registration as pure functions** (`RegisterResourceOps(api)`, `RegisterFrontDoorOps(api)`)
  so a generator can assemble the unified spec without running a server.
- **Generator + committed output + CI drift check:** `cmd/genopenapi` (`make openapi`) builds one
  huma API from both registrars and writes `docs/openapi.yaml`; CI runs `make openapi &&
  git diff --exit-code` so the committed spec can never drift from the types (and stays reviewable in
  PRs). For spec assembly the router needs only the proxied operations' *definitions* (types/paths),
  not live handlers.
- **Pipeline reorder:** `make openapi` runs first; `web` then runs `pnpm gen:api` + `pnpm docs:openapi`
  on the generated file (unchanged commands, new upstream).
- **Caveats:** (1) adopting huma is a real refactor of the current plain-chi + `internal/httpx`
  handlers — do it as its **own increment** (Increment 0), not bundled with the router cutover; huma
  mounts alongside chi so conversion is resource-by-resource. (2) Rich spec prose/examples move into
  struct tags + operation metadata. (3) The trust-seam contract tests are unaffected.
- **Lighter alternative (rejected unless deferring huma):** keep the spec hand-written and add a
  conformance test that validates live responses against `openapi.yaml` — *detects* skew rather than
  *preventing* it.

### D10 — Keystore portability is Layer 2 (deferred, not in this plan)

The keystore is gateway-local SQLite — *"lose it and every number must re-pair"*
(`whatsmeow-store.md`). That is what makes **removal** of a gateway non-trivial (a session's keys
live only on its box). True drain/rebalance/failover requires a **shared keystore** (whatsmeow
`sqlstore` on Postgres — it speaks Postgres/SQLite, **not** MySQL, so it is *separate* infra). The
`wastore.Keystore` consumer interface makes this a later implementation swap, not a rewrite. **Not
done here.** Layer 1 makes accounting clean *within* the local-keystore constraint.

---

## 4. Component changes

### 4.1 Router — `cmd/router/` + `internal/router/` (NEW)

- **Entrypoint** `cmd/router/main.go`: config (shared MySQL DSN, Redis URL, better-auth URL for
  JWKS, Ed25519 signing key, listen addr, CORS origins), composition root wiring the pieces below.
- **Authn middleware** — reuse `internal/authz.Authenticate(Tokens, Keys)` verbatim (JWKS JWT +
  api-key). Resolves `Principal` onto context.
- **Resolver** — `session → gateway_id` (`SessionRepo.Get`) → `gateway base_url + status`
  (`GatewayRepo`). **Org isolation (load-bearing):** the session's `organization_id` must equal the
  principal's org, else `404`/`403`. Gateway must be `active` (or the request 503s).
- **REST proxy** — `net/http/httputil.ReverseProxy` to the owning gateway's `base_url`. Strips the
  inbound `Authorization`, attaches the **Ed25519 internal assertion** (D3) bound to method/path/
  body/gateway/session.
- **Capability pre-gate (optional, defense-in-depth):** the router *may* also apply the capability
  gate before proxying since it holds the full principal; the gateway re-gates regardless.
- **Realtime (WS)** — ticket endpoint + WS upgrade handler (D5); refactored `internal/stream` core;
  live-connection registry; control-bus subscriber drops connections.
- **Control-bus subscriber** — `internal/controlbus` moved/owned here: evict api-key cache + drop WS.
- **Edge concerns** — CORS for `FRONTEND_ORIGINS`, request-id, recover, optional edge rate-limit
  (`wa:rl:*`), `/healthz` `/readyz` `/metrics`.
- **Placement helper** — `POST /sessions` create path consults the registry to choose a gateway,
  then proxies the create to it (which pins `gateway_id`).

### 4.2 Gateway — `internal/...` + `cmd/server/` (CHANGES)

- **Drop client-facing authn.** Replace the two-acceptor middleware mounting with an
  **internal-assertion middleware** that verifies the router's Ed25519 assertion (repointed
  `JWTVerifier` against the router's JWKS) + anti-replay nonce cache, then populates the principal.
- **Keep** `gates.go` capability gates + all `internal/store` org-scoped queries (read principal
  from the assertion).
- **Remove** `internal/controlbus` wiring from the gateway; remove the api-key positive cache, the
  JWT deny-list, and the api-key/JWKS *verifiers* used for end-user auth.
- **Remove** the client-facing `GET /api/v1/events` route (events now only published, not served).
- **EventSink** — wire the **direct-push-to-router** publisher (D6) by default.
- **Registry lifecycle (D8):**
  - On boot: upsert own `gateways` row (`id=GATEWAY_ID`, `base_url`, `status=joining→active`),
    start a heartbeat goroutine touching `last_seen_at` + `session_count`.
  - On `SIGTERM`: mark `draining`, stop accepting placements, finish in-flight, mark `drained`,
    then exit.
- **Session pinning becomes authoritative** for routing (it was "best-effort, forward-compat"):
  adoption must reliably record `gateway_id` so the router can resolve. (Still local-keystore bound;
  Layer 2 unchanged.)

### 4.3 Frontend — `web/` (CHANGES)

- Point the API client base URL at the **router**, not a gateway (`app/lib/api/*`).
- Realtime: replace the NDJSON `fetch`+`ReadableStream` client with a **WS client** that first
  fetches a ticket (`POST /api/v1/realtime/ticket`) then connects the WS with `?ticket=&session=&
  since=`.
- No change to better-auth config or the `ctrl:*` publish path (frontend stays publish-only).
- Regenerate the typed client + API docs after `openapi.yaml` changes (see §8 bookkeeping).

### 4.4 Shared packages

- `internal/authz` — keep verifiers (now router-only consumers) + gates (gateway consumer); add a
  router-assertion **minter** (router) and the gateway-side **assertion verifier** (can reuse
  `JWTVerifier`). Keep `Principal`/context shared.
- `internal/stream` — refactor into transport-agnostic core + NDJSON adapter + **WS adapter**.
- `internal/controlbus` — now consumed by the router.

---

## 5. Data model changes

New migration **`migrations/0004_gateways_lifecycle.{up,down}.sql`** (golang-migrate; gateway is the
sole writer of WA tables). Adds lifecycle to the existing `gateways` table (which already has
`base_url`, `last_seen_at`):

```sql
-- up
ALTER TABLE gateways
  ADD COLUMN status        VARCHAR(16)     NOT NULL DEFAULT 'active'  -- joining|active|draining|drained|unreachable
    AFTER label,
  ADD COLUMN session_count INT UNSIGNED    NOT NULL DEFAULT 0
    AFTER status,
  ADD COLUMN capacity      INT UNSIGNED    NULL                       -- soft cap for placement; NULL = unbounded
    AFTER session_count;

CREATE INDEX idx_gateways_status_seen ON gateways (status, last_seen_at);
```

```sql
-- down
DROP INDEX idx_gateways_status_seen ON gateways;
ALTER TABLE gateways
  DROP COLUMN capacity,
  DROP COLUMN session_count,
  DROP COLUMN status;
```

- `GatewayRepo` (`internal/store`) gains: `Register/Heartbeat/SetStatus/ListActive/PickForPlacement`
  (or equivalent) and surfaces `status`/`session_count`/`capacity`.
- After the migration: `cd web && pnpm db:introspect` to refresh the read-only WA Drizzle models
  (`app/lib/db/wa.ts`).

`wa_sessions.gateway_id` already exists (NOT NULL) — no schema change there.

---

## 6. Security model (threat-driven)

- **Public gateway is acceptable** because the gateway accepts *only* requests carrying a valid
  Ed25519 router assertion (D3). Asymmetric keys mean a compromised gateway cannot mint assertions.
- **Replay defense** = request binding (`method`/`path`/`bodyHash`/`aud`/`session`) + short `exp` +
  one-time `jti` nonce cache. Covered by tests (§7).
- **Org isolation becomes load-bearing at the router** (no second user-auth check at the gateway).
  The router's session→org match is therefore a hard gate, and the gateway still scopes every query
  by the asserted org (defense in depth at the *data* layer, not the auth layer).
- **Contract tests** (the trust seam): the existing R5 better-auth JWT + api-key contract tests move
  to the **router**. Add a **new contract test** for the router→gateway assertion: router mints,
  gateway verifies, replay is rejected, tampered body/path/aud is rejected, expired is rejected.
- **Frontend-down resilience** preserved at the router (cached JWKS + `apikey` table lookups).

---

## 7. Build increments (ship green at each step)

**Increment 0 — Code-first OpenAPI foundation (D11; orthogonal, do first)**

1. Add huma (chi adapter); create `internal/apitypes` (shared DTOs) and convert the gateway's
   existing handlers to huma operations resource-by-resource (mounts alongside chi).
2. `cmd/genopenapi` + `make openapi` generating `docs/openapi.yaml`; CI drift check
   (`make openapi && git diff --exit-code`).
3. Re-point `web` generation at the now-generated spec (commands unchanged: `pnpm gen:api`,
   `pnpm docs:openapi`). Verify the generated spec matches the current hand-written contract before
   deleting the hand-written one. Gates green both halves.

> Increment 0 is independent of the router and can land before or in parallel with Increment A. It
> establishes the shared-types foundation the router's front-door/realtime DTOs then plug into.

**Increment A — REST broker + auth termination + registry Layer 1**

1. Migration `0004_gateways_lifecycle` + `GatewayRepo` lifecycle methods + `pnpm db:introspect`.
2. Gateway: registry lifecycle (self-register, heartbeat, drain), authoritative `gateway_id` pin.
3. Ed25519 assertion: minter (router) + verifier/nonce-cache middleware (gateway); router JWKS
   endpoint; gateway repointed verifier. Contract test for the assertion.
4. `cmd/router` + `internal/router`: authn, resolver + org isolation, ReverseProxy, placement on
   `POST /sessions`, stranded-session 503.
5. Gateway: swap end-user authn middleware for the assertion middleware; remove
   `internal/controlbus` + api-key cache/deny-list + end-user verifiers from the gateway.
6. Frontend: API base URL → router.
7. Move R5 contract tests to the router. Gates green both halves.

**Increment B — WebSocket realtime cutover**

1. Refactor `internal/stream` into transport-agnostic core + WS adapter.
2. Router: `POST /api/v1/realtime/ticket` (scope-bearing, authz-at-mint: `RequireEvents` for
   session/organization, `RequireSuperAdmin` for firehose) storing resolved params in Redis
   `rt:ticket:{id}` with ~30s TTL; WS upgrade handler that **atomically `GETDEL`s** the ticket
   (single-use), subscribes per scope (`evt:{org}:{session}` / `evt:{org}:*` / `evt:*`), applies the
   `events` + optional `gateway` filters, registers in the live registry, control-bus drop, and
   `?since=` replay for org/session scopes (firehose = live-tail).
3. Gateway: wire direct-push `EventSink` to the router; remove client-facing `/events` route.
4. Frontend: WS realtime client (ticket → connect → `since=` resume).
5. Express the WS realtime + ticket endpoints in the shared types/registrars and **delete** the
   client-facing gateway `/events`; `make openapi` → `pnpm gen:api` + `pnpm docs:openapi`.
6. **Delete** the NDJSON transport once nothing references it.

**Increment C — dead-code sweep (checkpoint `sweep`)**

1. `grep` for the deleted symbols + an unused-code linter pass; remove orphaned helpers.
2. Delete dead v1 crypto api-key helpers (`internal/crypto/apikey.go`) and any v1 leftovers in the
   working tree (`CLAUDE.md`: v1-shaped duplicates are removable).
3. Reconcile `internal/httpx` against what huma subsumed — delete the superseded helpers, keep the
   rest. Confirm both halves green; tag `checkpoint/sweep`.

(Layer 2 keystore portability and multi-Redis split are **separate, later** efforts.)

---

## 8. Docs to update (bookkeeping rules — same change as the behavior)

| Doc | Update |
|---|---|
| **`docs/specs/router.md`** (NEW) | Full design of record for the router (this plan, distilled into a living spec). Add to the index. |
| `docs/specs/_V2-STATUS.md` | Add `router.md`; note relocations in trust/stream/queue. |
| `masterplan-mvp.md` | §4 (trust boundary now the router), §4.5 (routing now real), §11 (stream→router/WS), §12 (client→router, not client→gateway). |
| `docs/specs/trust-model.md` | Authn terminates at the router; gateway trusts Ed25519 assertion; control-bus consumer is the router; gateway drops `internal/controlbus`; authz split table. |
| `docs/specs/stream.md` | Transport moves to the router as **WebSocket** with ticket auth; gateways only publish; `?since=` + control-bus drop preserved on the router. |
| `docs/specs/queue.md` | Control-bus subscriber is the router (not gateways); events direct-push default / shared-Redis at HA; one-Redis still the default. |
| `docs/specs/store.md` | `gateways` lifecycle columns (`status`, `session_count`, `capacity`) + new index; `GatewayRepo` lifecycle methods; migration `0004`. |
| `docs/specs/session-manager.md` | `gateway_id` pin is now authoritative for routing; placement; graceful drain on `SIGTERM`; heartbeat. |
| `docs/specs/http-foundation.md` | Gateway loses client-facing authn + `/events`; gains internal-assertion middleware; router owns the public route surface + CORS; gateway stops serving the OpenAPI spec, the router serves it at `/api/v1/openapi.yaml` (D9). |
| `CLAUDE.md` | Reframe `docs/openapi.yaml` from "the gateway REST API" to the **public API contract of record, served by the router** (schemas implemented by gateway, front-door/realtime by router, client+docs generated by web). File stays at repo root but is now **generated** (D11): the bookkeeping flow changes from "edit the yaml" to "edit the Go types → `make openapi` → `pnpm gen:api` + `pnpm docs:openapi`". |
| `docs/specs/whatsmeow-store.md` | Note Layer-2 keystore-portability path (shared `sqlstore`/Postgres) as the future enabler of trivial removal; unchanged for now. |
| `docs/specs/frontend.md` | API base URL → router; WS realtime client + ticket flow. |
| `docs/openapi.yaml` (now **generated**, D11) | Express the realtime/ticket + WS operations and the removed client `/events` **in the shared Go types** (`internal/apitypes`) + route registrars, then `make openapi` to regenerate, then `cd web && pnpm gen:api` **and** `pnpm docs:openapi`. The yaml is no longer hand-edited. |
| `internal/apitypes` + `cmd/genopenapi` + `Makefile` (NEW, D11) | Shared DTOs; the generator; `make openapi` target + CI drift check. |
| `docs/specs/eventing.md` | If firehose gateway-filtering is wanted, add a `gatewaySource` field to the event envelope so the router can filter by producing gateway. |
| `docs/mvp-progress.md` | Log the locked decisions (router as trust boundary; Ed25519 internal assertion; WS+ticket; registry Layer 1; Layer 2 deferred) and milestone status. |
| `deploy/` | New router Dockerfile + compose service + `.env.example`: **router** gets the Ed25519 signing key + better-auth JWKS URL + MySQL/Redis + CORS; **gateway** swaps better-auth JWKS/api-key config for a single `ROUTER_JWKS_URL` (D3) + the internal assertion settings. Net: fewer knobs on the gateway. |
| `migrations/` | `0004_gateways_lifecycle.{up,down}.sql` (then `pnpm db:introspect`). |

---

## 9. Open questions / risks

- **Ticket transport: decided** — Redis-backed, scope-bearing, single-use via `GETDEL` (D5a).
  Rejected alternatives: the `Sec-WebSocket-Protocol` JWT trick (can't carry rich scope or enforce
  single-use) and a stateless signed ticket (single-use still needs a Redis used-set — same cost).
- **Firehose durable replay.** Firehose is live-tail only; `?since=` replay is org/session-scoped.
  If admins need backfill across orgs, design a bounded cross-org `event_log` reader separately.
- **Envelope `gatewaySource` tag** — required only if firehose must filter by producing gateway;
  decide before Increment B (touches `eventing.md` + the publish path).
- **Nonce store under gateway replicas.** In-memory is fine for single-instance gateways; a
  multi-replica gateway behind one LB needs a shared nonce store (Redis). Decide per deployment.
- **Capability gate location.** Plan keeps gates at the gateway (single source) with an optional
  router pre-gate. If the route→capability map proves cleaner centralized, move it to the router and
  drop gateway gates — but that couples the router to the route map.
- **Direct-push event ingest contract.** Define the gateway→router event ingest endpoint shape
  (batching, backpressure, at-least-once vs best-effort) in `router.md` before Increment B.
- **Clock skew.** Assertion freshness depends on NTP; document the skew tolerance.

## 10. Code to delete / consolidate (the cleanup payoff)

The intended result is a strongly net-negative diff on the auth, handler, and stream surfaces.
Targets, each verified against a `grep`/build before removal:

| Surface | Action | Notes |
|---|---|---|
| **Per-handler decode/validate/error boilerplate** | **delete** across every resource | huma's typed input/output + tags replace manual `httpx.DecodeJSON` + ad-hoc validation + `WriteError` mapping. Largest single deletion. |
| **Hand-written `docs/openapi.yaml`** | **delete the authored source** | replaced by the generated artifact (D11). The yaml still exists but is no longer hand-maintained. |
| **NDJSON stream transport** (`internal/stream` framing/handler) | **delete** | WS-only (D5); no consumer remains. Keep only the transport-agnostic replay/registry core. |
| **`JWTVerifier`** (`internal/authz/jwt.go`) | **keep as a shared module, both consumers** (not deleted) | per the §0 tie-break: router verifies better-auth JWKS, gateway verifies router JWKS — same module, different `*_JWKS_URL` (D3). Reuse, not duplication. |
| **Gateway end-user authn** (`apikey.go`, `apikey_remote.go`, `apikey_cache.go`, two-acceptor wiring, JWT deny-list) | **move to router; delete from gateway** | gateway keeps only `gates.go` + `context.go` + a small assertion-verify middleware. No copy left on the gateway. |
| **Gateway `internal/controlbus` wiring** | **delete from gateway** | subscriber owned by the router now (D7). |
| **Gateway browser CORS** (`internal/authz/cors.go` on the gateway) | **delete from gateway** | browsers hit the router, not the gateway; CORS lives on the router. |
| **Gateway client-facing `/events` route + its router wiring** | **delete** | events are published, not served (D5/D6). |
| **Dead v1 crypto api-key helpers** (`internal/crypto/apikey.go`: `GenerateAPIKey`/`VerifyAPIKey`/argon2id, `wak_`) | **delete** | already flagged dead in `http-foundation.md`; remove in the sweep. |
| **Any v1 leftovers in the working tree** | **delete** | `CLAUDE.md` notes v1-shaped duplicates are removable; the sweep (§11 final checkpoint) hunts them with `grep` + dead-code detection. |
| **Hand-rolled `internal/httpx` helpers** superseded by huma | **consolidate/delete** | re-evaluate `DecodeJSON*`, error-envelope, pagination, list-envelope after huma lands; delete whatever huma subsumes, keep what it doesn't. |

A short "before/after LOC" line per checkpoint in the commit body makes the cleanup legible.

## 11. Git checkpoint strategy

Single branch `feat/central-router`; many small **green** commits; cheap rollback at boundaries.

- **Every commit is green** — `go build ./... && go vet ./... && go test ./...` and (when `web/` is
  touched) `pnpm build && pnpm typecheck && pnpm test`. Never commit a red tree.
- **Three-commit refactor rhythm** for each moved/replaced responsibility, so deletions are isolated:
  1. `feat:` add the replacement (new path), tests green alongside the old path.
  2. `refactor:` switch callers to the replacement.
  3. `refactor:` **delete** the old path (this is the commit that shows the negative LOC).
- **Checkpoint tags at increment boundaries** — e.g. `git tag checkpoint/inc0`, `checkpoint/incA`,
  `checkpoint/incB`, `checkpoint/sweep` — so any increment can be reverted wholesale without
  untangling later work.
- **Spec/docs travel with the behavior** in the *same* commit (bookkeeping rule, §8): a handler change
  that alters a shape regenerates the spec + client + docs in that commit.
- **Final dead-code sweep checkpoint** — after Increment B, a dedicated pass: `grep` for the deleted
  symbols, run an unused-code linter, remove orphaned helpers/v1 leftovers, confirm both halves green.
  This is where the last of the deletion inventory (§10) is collected.
- **Commit messages** use Conventional-Commits (`feat:`/`refactor:`/`docs:`/`chore:`) and note the
  net LOC delta when it's a deletion commit.

Suggested checkpoint sequence: `inc0` (huma + generated spec, big handler-boilerplate deletion) →
`incA` (router + auth termination, gateway authn deleted) → `incB` (WS cutover, NDJSON deleted) →
`sweep` (v1/dead-code removal). Each is independently green and revertable.

## 12. Out of scope (explicitly)

- **Layer 2 — keystore portability** (shared `sqlstore`/Postgres, live session re-homing,
  auto-failover, rebalancing). Documented as the future upgrade in `whatsmeow-store.md`.
- **Multi-Redis split** (`PUBSUB_REDIS_URL` dedicated bus) — env change when HA is actually needed.
- **Multi-router HA** beyond making the events seam swap-ready (D6).
</content>
</invoke>
