# MVP Progress Tracker

Tracks implementation status against [`masterplan-mvp.md`](plans/masterplan-mvp.md).
Last updated: 2026-08-07.

> **Pivot to v2 (split architecture).** The single-binary v1 MVP (Go + Authula + embedded
> React Router SPA + MySQL keystore) was **code-complete (M0–M8)** and is preserved at git
> tag `mvp-v1`. The project is now revamping to a
> **gateway (Go) + fullstack frontend (TanStack Start + better-auth)** split. v2 milestones
> (R0–R5, masterplan §17) below.

## v2 revamp status (R-milestones)

| Milestone | Status | Notes |
|---|---|---|
| **R0** — Snapshot & specs | ✅ Done | v1 archived + tagged `mvp-v1`; masterplan rewritten to v2; `docs/specs/*` carried superseded banners + the `_V2-STATUS.md` index (full rewrites landed with each R-milestone, finalized in R5). |
| **R1** — Gateway de-auth | ✅ Done | **Historical R1 state, superseded by the central router:** `internal/auth` (Authula) was removed and `internal/authz` added; ownership changed `tenant_id`→`organization_id`; `/auth` and `/keys` disappeared; JWT/API-key verification, CORS, the positive key cache, and `ctrl:*` subscription initially lived on each gateway. They now live on the router. Fresh v2 `migrations/0001_init`. |
| **R2** — Keystore → SQLite | ✅ Done | whatsmeow `sqlstore` on `modernc.org/sqlite` (CGO=0); persistent `/data/keystore` volume; `gateways` self-row + `wa_sessions.gateway_id` pinning; boot orphan-guard (skip+`STOPPED` sessions whose org is gone); admin number re-paired against SQLite. |
| **R3** — Frontend scaffold | ✅ Done | TanStack Start app; better-auth (email/password, twoFactor, admin, apiKey, jwt, **organization**) on MySQL via `drizzleAdapter`; auth tables via drizzle-kit; WA tables read-only Drizzle models; `definePayload` → `activeOrganizationId`+`orgRole`+`role`; **personal-org-on-signup** hook; shadcn `components/ui` ported; SPA logic re-fit to TanStack Start idioms (loaders/`createServerFn`, `createMiddleware`/`beforeLoad`, file-based routing); login/register/TOTP/admin/keys + **org switcher**. |
| **R4** — Frontend ↔ gateway | ✅ Done | **Historical R4 state, superseded by the central router:** browsers initially called gateways directly and consumed NDJSON with a bearer JWT. Today browsers call the router and consume its ticketed WebSocket; gateway NDJSON and public auth/CORS are removed. Direct-MySQL display reads and better-auth `ctrl:*` publishers remain. |
| **R5** — Packaging & docs | ✅ Done | Historical split packaging/docs milestone. The API contract subsequently moved from hand-maintained YAML to generated Huma OpenAPI served by the router; central-router contract tests supersede the original direct-gateway trust smoke. |
| **R6** — Collaboration | ⬜ Remaining (fast-follow) | Members & invitations UI on the org plugin (invite by email, accept/reject, role change, remove member); publish `ctrl:member.removed` on removal. Additive — ownership/org plumbing already shipped in R1/R3. |

## Central router (post-R5 — `feat/central-router`)

Plan: [`plans/plan-router-impl.md`](plans/plan-router-impl.md). Spec: [`specs/router.md`](specs/router.md).

| Increment | Status | Notes |
|---|---|---|
| **Increment A** — REST broker + auth termination + registry Layer 1 | ✅ Done | Historical router/proxy and gateway lifecycle increment. Its former `0004_gateways_lifecycle` schema was folded into the clean `0001` baseline by gRPC Increment 2.0; runtime `Heartbeat/SetStatus/ListActive/PickForPlacement` behavior remains. |
| **Increment B** — WebSocket realtime cutover | ✅ Done | Router ticket mint + single-use Redis `GETDEL` redemption, WebSocket endpoint, replay/tail pump, frontend WS client, and live stream-drop on revocation are implemented. Gateways publish `evt:*` over shared Redis; gateway NDJSON `/events` is removed. Direct gateway→API ingest is deferred to acknowledged gRPC eventing. |
| **Increment 0** — code-first OpenAPI (Huma) | ✅ Done | Shared Go DTOs/Huma operations generate `docs/openapi.yaml`; the router serves the generated contract and drift is checked by `make openapi-check`. |

## gRPC control-plane migration (`migration/grpc-control-plane`)

Plan: [`plans/plan-grpc-control-plane.md`](plans/plan-grpc-control-plane.md). The target is locked,
but the current router/proxy, gateway MySQL/Redis dependencies, and Ed25519 assertion remain live
until later increments replace them.

| Increment | Status | Notes |
|---|---|---|
| **Increment 0** — decisions and contract tooling | ✅ Done | Separate public/private Buf modules; reproducible generation and compatibility/drift checks; health anchors; initial transport-independent engine ports; target boundary and operational defaults. |
| **Increment 1** — composition roots + public health | ✅ Done | API/gateway roots, binaries, images, Compose services, and config identities renamed. API public HTTP `8090` and gRPC `8081` listeners share readiness. WA schema migration ownership moved to API startup plus `cmd/migrate`; transitional gateway HTTP remains private. |
| **Increment 2.0–2.2** — enrollment persistence, PKI, service | ✅ Done | Normalized registry/enrollment/certificate/audit schema; token/CSR and envelope policy; persistent local root/intermediate signer; nonce/lease-fenced enrollment with exact same-CSR replay. |
| **Increment 2.3 + bootstrap** — private enrollment transport | ✅ Done | Opt-in TLS 1.3 listener, anonymous access restricted to enrollment, strict live certificate authorization elsewhere, atomic API identity, and crash-safe gateway credential bootstrap over one reusable mTLS connection. |
| **Increment 2 control contract** — stream protocol | ✅ Done | Private bidi hello/welcome, heartbeat/lifecycle, version/sequence, connection-epoch, and lifecycle-directive contract with generated bindings and compatibility tests. |
| **Increment 2 control persistence** — DB fencing | ✅ Done | Atomic compare-and-swap epoch allocation stores Hello metadata; rejects non-connectable gateways; and fences heartbeat/lifecycle/metadata writes from stale streams. Accept/heartbeat preserve applied revision, lifecycle preserves session count, and `degraded` is a durable runtime status; reports cannot select administrative states. |
| **Increment 2 control runtime** — durable liveness | ✅ Done | API Hello/heartbeat watchdogs and post-persistence HeartbeatAck are implemented. The wired gateway supervisor validates sequences/epochs, sends one heartbeat per durable ack, reconnects transient failures, and gates readiness on acknowledged READY/RUN state. Control-enabled mode exclusively owns registry liveness and skips the five legacy joining/active/heartbeat/draining/drained writes; control-disabled mode retains them. |
| **Increment 2 lifecycle bridge** — transitional drain/routing | ✅ Done | Registry rows separate observed status, admin-owned desired RUN/DRAIN, and explicit control/legacy mode. Placement requires desired RUN; mode selects 15s/90s freshness. Irreversible engine admission and Asynq startup require acknowledged RUN+READY; drain waits admitted requests and joins workers before manager shutdown. The lifetime watcher covers delayed Boot and terminal supervisor errors exit cleanly. SIGTERM uses reconnect-safe Flush acknowledgements; disconnect cleanup is detached and capped at 5s. |
| **Increment 2 remainder** — directives/renewal/admin | ✅ Done | Strict epoch-fenced lifecycle directives/reports drive terminal drain/disable behavior. Automatic renewal stages credentials and proves an overlapping replacement stream through Welcome plus durable heartbeat acknowledgement before retiring the incumbent. API-local audited administration and the `super_admin` web workflow cover create/enroll/observe/lifecycle/re-enroll/delete; plaintext enrollment tokens remain one-response-only. |
| **Increment 3** — desired-state reconciliation | ✅ Done | Atomic revisioned assignment/config snapshots carry per-session epochs and renewable leases. Gateways reconcile local inventory without MySQL lifecycle reads, stop stale work, report durable missing/corrupt/unexpected outcomes, and gate readiness/placement on current healthy reconciliation. Persistent-path validation, SQLite integrity inspection, checkpointed shutdown, operator recovery guidance, and admin API/web observability are implemented. |
| **Increment 4** — first unary engine slices | ✅ Complete | Session state, account presence, and read receipts execute through the private engine service with mTLS, assignment fencing, and API-side pooling. |
| **Increment 5** — reliable gateway event ingestion | ✅ Complete | Journaled gateway events stream over the control stream with commit-gated acknowledgements, fenced transactional ingest + dedup in MySQL, and a leased post-commit worker fanning out to realtime/webhooks. Poll-recap emission is API-owned. Journal pressure reports on heartbeats into admin observability; legacy fan-out remains for control-disabled mode until Increment 9. |
| **Increments 5+** — reliable event/command cutover | ⬜ Planned | Add reliable events/commands, remaining resources, public application gRPC, then remove gateway HTTP/MySQL/Redis and harden the sole target deployment. |

## v1 milestones (archived — code complete)

All M0–M8 done as of commit `2ca7467` (tag `mvp-v1`). Full detail in
[`archive/mvp-progress-v1.md`](archive/mvp-progress-v1.md). The only open v1 item was a manual
e2e smoke against a live WhatsApp number.

## Key v2 decisions (locked this session)

- **gRPC target boundary (Increment 0; not runtime yet):** the API/control plane becomes the only
  public front door and owns REST/Huma/OpenAPI, public gRPC, end-user authn/authz, MySQL, Redis,
  application services, placement, jobs, realtime, webhooks, and event ingestion. Gateways become
  private whatsmeow engines with local keystore, command ledger, and event journal; they receive no
  user credentials and require neither shared MySQL nor Redis after cutover.
- **Initial topology:** API → gateway commands use pooled unary gRPC over persistent HTTP/2, and
  gateways open a long-lived control/event stream. Every initially supported topology must expose an
  API-addressable gateway engine endpoint; outbound-only reverse-command mode is deferred.
- **mTLS CA seam:** development uses a persisted local root plus online intermediate, with the root
  signing only the intermediate. Production certificate issuance is behind `CertificateSigner`;
  Vault PKI is the reference implementation, not a vendor lock. Exact production CA remains a
  deployment choice.
- **Private enrollment transport:** `gateway.v1.GatewayEnrollmentService`, the exact API SPIFFE
  server-leaf policy, crash-recoverable API TLS identity, private TLS 1.3 listener, enrollment
  adapter, certificate authorization, and gateway bootstrap client are implemented and opt-in.
- **Durable handoff storage:** gateway event/command state lives in a separate `journal.db` on the
  same persistent volume, never in whatsmeow-owned tables. Configurable initial defaults are a 72h
  outage sizing objective (not guaranteed RPO), 1 GiB cap with configuration rejected above 25% of
  volume budget, degraded/pause-optional/critical thresholds at 70/80/90%, at most 256 or 1 MiB
  events in flight, and seven-day command-ledger retention that must cover the API retry/idempotency
  window. Increment 5 soak results determine production tuning.
- **Protobuf policy:** `public.v1` and `gateway.v1` are separate Buf modules/compatibility domains;
  both use `FILE` breaking policy. Go bindings are committed and drift is checked by regenerating in
  a temporary directory. BSR adoption remains open and is not required for local/CI checks.
- **Initial application seam:** session state, account presence, and read receipts are the first
  transport-independent gateway-engine ports. Their local WA adapter is not wired into any call
  path. Mutation metadata is carried now, while durable command idempotency and assignment-epoch
  validation remain explicit later-increment work.
- **Pre-release migration freedom:** no production backward compatibility is required. Architecture,
  packages, schemas, public APIs, and deployment configuration may be reshaped directly toward the
  clean gRPC target without compatibility shims; every increment must still build, test, and deploy.

- **Central router is the single trust boundary + front door (Increment A):** end-user authn
  (better-auth JWT via JWKS / api-key vs the shared `apikey` table) and the `ctrl:*` control-bus
  subscriber moved **off the gateway to the router** (`cmd/router` + `internal/router`, stateless).
  Callers use one base URL + token + session id; the router resolves the session's owning gateway
  from `wa_sessions.gateway_id`, enforces **org isolation** (session org == caller org else `404`;
  super_admin bypasses), and reverse-proxies. Routing rules: `POST /sessions` → placement
  (least-loaded `active`); session-specific path → owning gateway; everything else → any `active`;
  stranded session → `503 gateway_unavailable`. The gateway now authenticates nothing end-user-facing
  and no longer mounts CORS or serves the OpenAPI spec.
- **Router→gateway trust seam = request-bound, single-use Ed25519 assertion (`internal/assertion`):**
  the router strips the caller credential and attaches `X-Internal-Assertion` (a compact JWS bound to
  `aud`=gateway, `method`/`path`/`bodyHash`, `session`, `jti` nonce, ~30s `exp`, + the resolved
  principal). Router holds the private key (`Minter`) and publishes its JWKS at
  `/.well-known/router-jwks.json`; the gateway holds only the public key (verify order: signature →
  `aud` → `iss` → exp/iat skew → method/path/bodyHash → `jti` anti-replay via in-memory `NonceCache`),
  so a compromised gateway cannot forge assertions. **Authz split unchanged in spirit:** verify at the
  router, **gate + scope at the gateway** (capability gates + org-scoped queries read the asserted
  principal). *Deviation:* the gateway uses a dedicated jwx verifier in `internal/assertion` (extra
  request-binding claims) rather than the plain `JWTVerifier`, reusing the JWKS-cache pattern at
  `ROUTER_JWKS_URL`.
- **Gateway registry lifecycle = Layer 1 (Increment A):** `gateways` gains `status`
  lifecycle + `session_count` + `capacity` + `idx_gateways_status_seen` (now folded into `0001`);
  boot registers `joining→active`, a
  30s heartbeat writes `last_seen_at`+`session_count`, SIGTERM drains `draining→drained`.
  `wa_sessions.gateway_id` is **authoritative for routing**. Keystore portability (Layer 2 — live
  re-homing on a shared `sqlstore`/Postgres) is **deferred**.
- **Realtime and OpenAPI are router-owned:** browsers mint a scoped ticket and connect to the
  router WebSocket; gateways publish current runtime events through shared Redis `evt:*`. Gateway
  NDJSON is removed. Huma operations/Go DTOs generate `docs/openapi.yaml`, which the router serves.
  Direct acknowledged gateway→API event ingest is part of the gRPC migration, not current runtime.
- **Current auth boundary:** humans present better-auth JWTs and machines present better-auth API
  keys to the router. The router verifies both, owns CORS and the positive key cache, and forwards a
  request-bound internal assertion; gateways do not verify public credentials.
- **Data:** shared MySQL — frontend writes auth tables, gateway currently writes WA-domain tables;
  **hybrid reads** use direct MySQL for frontend display, router-mediated REST for actions, and the
  router WebSocket for realtime. Keystore is **gateway-local SQLite** on a persistent volume.
- **API-key revocation:** better-auth publishes `ctrl:*`; the router subscriber evicts its positive
  key cache and drops affected WebSockets. The ~60-second cache TTL is the missed-message backstop.
  Gateways neither cache public keys nor subscribe to the public-auth control bus.
- **Ownership = organizations:** resources owned by **`organization_id`** (better-auth
  organization), not `user_id`; **personal org per user** auto-created on signup; org roles
  owner/admin/member gate access; JWT carries `activeOrganizationId`+role. Collaboration
  (invite to co-manage a connection) via the org plugin — plumbing in v2 (R1/R3), invite/members
  UI is R6. (Masterplan §4, §7, §12.)
- **JWT refresh = the session:** no separate refresh token; the better-auth **session** is the
  long-lived revocable credential, the JWT is a **5-min** access token minted at
  `/api/auth/token`. Revoke the session → refresh stops; `ctrl:user.banned`/`session.revoked`
  kills in-flight JWTs instantly. (Masterplan §4.7.)
- **Serverless frontend:** the browser talks to the **router** for actions and its ticketed
  WebSocket. The frontend server owns better-auth/JWT minting and direct MySQL display reads, but
  does not proxy long-lived realtime connections.
- **Plugin set kept minimal:** email/password, twoFactor, admin, apiKey, jwt, organization.
  Magic-link / passkey / captcha deferred.
- **Frontend DB layer = Drizzle:** better-auth runs on the **`drizzleAdapter`** (provider
  `mysql`); the same Drizzle client serves the read-only WA queries. Auth tables: schema via
  `@better-auth/cli generate`, migrated with **drizzle-kit** (not the Kysely-only better-auth
  `migrate`). WA tables: read-only Drizzle models **introspected** from the API-migrated DB
  (API-owned golang-migrate stays the sole schema writer). (Masterplan §6.2, §12, §19 #5.)
- **Clean-slate migrations:** pre-release, so v2 rewrites `migrations/` from scratch against an
  empty DB — no v1→v2 backfill. (Masterplan §7.)
- **Gateway visibility:** session API responses + dashboard show each session's `gatewayId` and
  the gateway's label/status (from the `gateways` registry). (Masterplan §12, §13.)
- **Docs refresh is a tracked requirement:** stale `docs/specs/*` carry superseded banners +
  a [`specs/_V2-STATUS.md`](specs/_V2-STATUS.md) index now; full per-spec rewrites land with
  their owning R-milestone. (Masterplan §17 R0.)
- **Forward-compat:** `gateways` registry + `wa_sessions.gateway_id` so multi-gateway is
  additive.
- **Identity model (single central table):** one `whatsapp_identities` row per person,
  keyed by the **canonical non-AD LID** (the `:device` suffix is stripped at capture and
  backfill, collapsing the duplicate rows that made identities inconsistent). The
  per-session `whatsapp_contacts` table is **removed** — "found in DM" is derived from the
  `chats` table; group membership is the `whatsapp_group_members` **pivot** (identity ↔
  group) carrying `role` + a per-group **`tag`** (the second per-group identity WhatsApp
  shows beside the push name). Backfill seeds identities for every group participant and
  resolves push names from the contact store. (Migration `0002_identity_redesign`; identity
  tables wiped for a clean re-backfill. See `docs/specs/contacts.md`, `resources.md`.)
- **User backup import (crypt15):** ordinary org users can backfill a session's full history by
  uploading their WhatsApp `msgstore.db.crypt15` + key (`POST /sessions/{session}/backfill`), not
  just the admin live-data backfill. Gateway-side decrypt (CGO-free, `internal/backup`) + SQLite
  read via `modernc.org/sqlite` with **capability detection** (no reliable WhatsApp schema version
  exists — probe tables/columns, degrade gracefully, record a fingerprint). Upserts chats /
  messages / identities / groups / members idempotently by `(session_id, wa_message_id)`, so it
  merges with live capture. **Quota locked: once per 24h per session for non-admins; super_admin
  unlimited** — enforced durably via the new `backfill_imports` table (Migration
  `0003_backfill_imports`). See `docs/specs/backfill-import.md`.

## Open risks / follow-ups

- **better-auth api-key hash replicability** — RESOLVED for the pinned version: better-auth
  1.6.22's default hash is `base64url(SHA-256(rawKey))` unpadded, replicated in
  `internal/authz` and locked by the R5 contract test. A major-version bump must re-run that
  test; the `/api/auth/api-key/verify` remote fallback stays available. (Masterplan §4.2, §19.)
- **`docs/specs/*` rewrite** — COMPLETE at R5: every spec is v2, [`specs/_V2-STATUS.md`](specs/_V2-STATUS.md)
  is all-green.
- **R6 collaboration UI** — members/invitations UI is the remaining fast-follow; org plumbing
  already shipped.
- Private gRPC enrollment transport and gateway credential bootstrap are implemented and opt-in,
  including pinned SPIFFE/root verification, pending CSR/key reuse, replay-aware retries, strict
  certificate authorization, and a reusable mTLS connection.
- The private bidi control contract is implemented. API-side stream handling and database epoch
  fencing, durable heartbeat acknowledgements, Hello/lease watchdogs, gateway reconnect
  supervision, and readiness gating are implemented. Control mode owns registry writes exclusively;
  the legacy five-write path remains only when control is disabled. Authenticated transitional HTTP
  addressing, fenced disconnect liveness, observed/desired lifecycle separation, explicit
  connection-mode freshness, full engine-route admission draining, deferred RUN boot, and
  reconnect-safe acknowledged shutdown heartbeats are implemented. Strict post-Welcome lifecycle
  directives/reports, renewal with overlapping-stream proof, revocation-driven termination, audited
  administration, and the one-time-token web workflow are implemented. Increment 2 is complete;
  desired-state reconciliation is the active Increment 3 boundary.
