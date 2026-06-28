# MVP Progress Tracker

Tracks implementation status against [`masterplan-mvp.md`](../masterplan-mvp.md).
Last updated: 2026-06-27.

> **Pivot to v2 (split architecture).** The single-binary v1 MVP (Go + Authula + embedded
> React Router SPA + MySQL keystore) was **code-complete (M0–M8)** and is preserved at git
> tag `mvp-v1`. The project is now revamping to a
> **gateway (Go) + fullstack frontend (TanStack Start + better-auth)** split. v2 milestones
> (R0–R5, masterplan §17) below.

## v2 revamp status (R-milestones)

| Milestone | Status | Notes |
|---|---|---|
| **R0** — Snapshot & specs | ✅ Done | v1 archived + tagged `mvp-v1`; masterplan rewritten to v2; `docs/specs/*` carried superseded banners + the `_V2-STATUS.md` index (full rewrites landed with each R-milestone, finalized in R5). |
| **R1** — Gateway de-auth | ✅ Done | `internal/auth` (Authula) removed; `internal/authz` added (JWKS+JWT verify via `jwx/v3`, api-key verify vs shared `apikey`); ownership `tenant_id`→`organization_id`; `tenants`/`api_keys` dropped; `/auth`+`/keys` routes gone; CORS for `FRONTEND_ORIGINS`; per-gateway key cache + `ctrl:*` control-bus subscriber + boot reconcile. Fresh v2 `migrations/0001_init`. |
| **R2** — Keystore → SQLite | ✅ Done | whatsmeow `sqlstore` on `modernc.org/sqlite` (CGO=0); persistent `/data/keystore` volume; `gateways` self-row + `wa_sessions.gateway_id` pinning; boot orphan-guard (skip+`STOPPED` sessions whose org is gone); admin number re-paired against SQLite. |
| **R3** — Frontend scaffold | ✅ Done | TanStack Start app; better-auth (email/password, twoFactor, admin, apiKey, jwt, **organization**) on MySQL via `drizzleAdapter`; auth tables via drizzle-kit; WA tables read-only Drizzle models; `definePayload` → `activeOrganizationId`+`orgRole`+`role`; **personal-org-on-signup** hook; shadcn `components/ui` ported; SPA logic re-fit to TanStack Start idioms (loaders/`createServerFn`, `createMiddleware`/`beforeLoad`, file-based routing); login/register/TOTP/admin/keys + **org switcher**. |
| **R4** — Frontend ↔ gateway | ✅ Done | Browser→gateway direct (actions + NDJSON stream) with `Bearer` JWT; server mints JWT (`mintGatewayToken`) + direct-MySQL reads for dashboards/viewer/contacts; webhook config via gateway API; control bus publishes `ctrl:apikey.revoked`/`user.banned`/`member.removed` from better-auth `after` hooks. **Trust seam validated LIVE** against better-auth 1.6.22. |
| **R5** — Packaging & docs | ✅ Done | Two Dockerfiles + split compose + `.env.example`; `openapi.yaml` (auth/keys paths dropped); README rewritten to the v2 split; **all `docs/specs/*` rewritten to v2** (`_V2-STATUS.md` all-green); contract tests (better-auth JWT ↔ gateway verify; better-auth api-key ↔ gateway verify); e2e smoke (login → mint JWT → start session → pair → send → stream). |
| **R6** — Collaboration | ⬜ Remaining (fast-follow) | Members & invitations UI on the org plugin (invite by email, accept/reject, role change, remove member); publish `ctrl:member.removed` on removal. Additive — ownership/org plumbing already shipped in R1/R3. |

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
