# MVP Progress Tracker

Tracks implementation status against [`masterplan-mvp.md`](../masterplan-mvp.md).
Last updated: 2026-06-26.

> **Pivot to v2 (split architecture).** The single-binary v1 MVP (Go + Authula + embedded
> React Router SPA + MySQL keystore) is **code-complete (M0–M8)** and **archived**: see
> [`archive/`](archive/README.md) and git tag `mvp-v1`. The project is now revamping to a
> **gateway (Go) + fullstack frontend (TanStack Start + better-auth)** split. v2 milestones
> (R0–R5, masterplan §17) below.

## v2 revamp status (R-milestones)

| Milestone | Status | Notes |
|---|---|---|
| **R0** — Snapshot & specs | 🟡 In progress | v1 archived + tagged `mvp-v1`; masterplan rewritten to v2. **TODO:** update `docs/specs/*` (`auth-tenancy.md`→trust-model, `whatsmeow-store.md`→SQLite, `frontend.md`→TanStack Start). |
| **R1** — Gateway de-auth | ⬜ Planned | Rip out `internal/auth` (Authula); add `internal/authz` (JWKS+JWT verify, api-key verify vs `apikey`); `tenant_id`→`user_id`; drop `tenants`/`api_keys`; remove `/auth`+`/keys` routes; add CORS. |
| **R2** — Keystore → SQLite | ⬜ Planned | whatsmeow `sqlstore` on `modernc.org/sqlite`; persistent volume; add `gateways` + `wa_sessions.gateway_id`; re-pair admin number. |
| **R3** — Frontend scaffold | ⬜ Planned | TanStack Start app; better-auth (email/password, twoFactor, admin, apiKey, jwt, **organization**) on MySQL + migrations; `definePayload` → `activeOrganizationId`+role in JWT; **personal-org-on-signup** hook; copy shadcn `components/ui`; **re-fit SPA logic to TanStack Start idioms** (loaders/`createServerFn`, `createMiddleware`/`beforeLoad`, file-based routing); port login/register/TOTP/admin/keys; **org switcher**. |
| **R4** — Frontend ↔ gateway | ⬜ Planned | Direct browser→gateway (actions + stream) with `Bearer` JWT; server mints JWT + does direct-MySQL reads; webhook config via gateway API; publish `ctrl:apikey.revoked`/`user.banned`/`member.removed` on revoke/ban/remove. |
| **R5** — Packaging & docs | ⬜ Planned | Two Dockerfiles + split compose; `.env.example`; `openapi.yaml` (drop auth/keys); README; contract tests (JWT + api-key); e2e smoke. |
| **R6** — Collaboration | ⬜ Planned (fast-follow) | Members & invitations UI on the org plugin (invite by email, accept/reject, role change, remove); additive — ownership/org plumbing landed in R1/R3. |

## v1 milestones (archived — code complete)

All M0–M8 done as of commit `2ca7467` (tag `mvp-v1`). Full detail in
[`archive/mvp-progress-v1.md`](archive/mvp-progress-v1.md). The only open v1 item was a manual
e2e smoke against a live WhatsApp number.

## Key v2 decisions (locked this session)

- **Auth boundary:** humans → better-auth **JWT** verified by the gateway via **JWKS**
  (`/api/auth/jwks`); machines → better-auth **api-key** plugin, validated by the gateway
  against the shared `apikey` table. No per-request gateway→frontend callback.
- **Data:** shared MySQL — frontend writes auth tables, gateway writes WA-domain tables;
  **hybrid reads** (frontend reads WA tables directly for display, acts via gateway API,
  realtime via gateway stream). Keystore moves to **gateway-local SQLite** (persistent volume).
- **API-key revocation:** **instant** via a cross-service Redis **control bus** (`ctrl:*`
  pub/sub) — frontend publishes on revoke/ban, all gateways evict their cache + drop live
  streams; ~60-s cache TTL is the backstop, and a **boot-time reconcile sweep** catches up on
  messages missed while a gateway was down (orphan-guard sessions + prune stale keys). Two Redis
  roles: `REDIS_URL` (work) + `PUBSUB_REDIS_URL` (control bus, defaults to `REDIS_URL` for
  single-instance dev), namespaced by `REDIS_PREFIX` to avoid collisions. (Masterplan §4.6.)
- **Ownership = organizations:** resources owned by **`organization_id`** (better-auth
  organization), not `user_id`; **personal org per user** auto-created on signup; org roles
  owner/admin/member gate access; JWT carries `activeOrganizationId`+role. Collaboration
  (invite to co-manage a connection) via the org plugin — plumbing in v2 (R1/R3), invite/members
  UI is R6. (Masterplan §4, §7, §12.)
- **JWT refresh = the session:** no separate refresh token; the better-auth **session** is the
  long-lived revocable credential, the JWT is a **5-min** access token minted at
  `/api/auth/token`. Revoke the session → refresh stops; `ctrl:user.banned`/`session.revoked`
  kills in-flight JWTs instantly. (Masterplan §4.7.)
- **Serverless frontend:** the gateway owns all long-lived connections; the **browser talks to
  the gateway directly** (Bearer JWT) for actions **and** the NDJSON stream — no frontend proxy
  — so the frontend hosts on serverless. Frontend server only does auth, JWT minting, and direct
  MySQL reads. (Masterplan §12, §19 #2.)
- **Plugin set kept minimal:** email/password, twoFactor, admin, apiKey, jwt, organization.
  Magic-link / passkey / captcha deferred.
- **Frontend DB layer = Drizzle:** better-auth runs on the **`drizzleAdapter`** (provider
  `mysql`); the same Drizzle client serves the read-only WA queries. Auth tables: schema via
  `@better-auth/cli generate`, migrated with **drizzle-kit** (not the Kysely-only better-auth
  `migrate`). WA tables: read-only Drizzle models **introspected** from the gateway-migrated DB
  (the gateway's golang-migrate stays the sole writer). (Masterplan §6.2, §12, §19 #5.)
- **Clean-slate migrations:** pre-release, so v2 rewrites `migrations/` from scratch against an
  empty DB — no v1→v2 backfill. (Masterplan §7.)
- **Gateway visibility:** session API responses + dashboard show each session's `gatewayId` and
  the gateway's label/status (from the `gateways` registry). (Masterplan §12, §13.)
- **Docs refresh is a tracked requirement:** stale `docs/specs/*` carry superseded banners +
  a [`specs/_V2-STATUS.md`](specs/_V2-STATUS.md) index now; full per-spec rewrites land with
  their owning R-milestone. (Masterplan §17 R0.)
- **Forward-compat:** `gateways` registry + `wa_sessions.gateway_id` so multi-gateway is
  additive.

## Open risks / follow-ups

- **better-auth api-key hash replicability** — the gateway validating keys by direct DB
  lookup depends on better-auth's deterministic hashing. Pin the better-auth version + add a
  contract test; fallback is `/api/auth/api-key/verify`. (Masterplan §4.2, §19.)
- **`docs/specs/*` rewrite in progress** — superseded banners + [`specs/_V2-STATUS.md`](specs/_V2-STATUS.md)
  index landed (R0); each spec is fully rewritten with its owning R-milestone. Until then, the
  masterplan is the source of truth.
