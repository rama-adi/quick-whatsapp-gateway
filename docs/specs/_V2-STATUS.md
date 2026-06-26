# docs/specs — v2 status & refresh tracker

These subsystem specs were written for the **v1** single-binary MVP (Go + Authula + embedded
React Router SPA + MySQL keystore). The project has pivoted to **v2** (gateway + TanStack
Start/better-auth split — see [`../../masterplan-mvp.md`](../../masterplan-mvp.md)). Until each
spec is rewritten, **the masterplan is the source of truth** where they disagree.

**Rule (masterplan §17 R0 / §20):** no spec may describe removed v1 behavior without a
*superseded* banner. Each spec is fully rewritten **in the same change** as the R-milestone
that re-implements its subsystem.

| Spec | v2 disposition | Owning milestone | Notes |
|---|---|---|---|
| `auth-tenancy.md` | **Replace** → `trust-model.md` | R1 | Authula gone. JWKS-verified JWTs + better-auth api-keys + org ownership (§4). |
| `api-keys.md` | **Rewrite** | R1/R3 | No custom Go keys; better-auth **api-key** plugin, gateway verifies vs shared `apikey` (§4.2). |
| `whatsmeow-store.md` | **Replace / retire** | R2 | Custom MySQL store dropped; SQLite via whatsmeow `sqlstore` on `modernc.org/sqlite` (§6.1). |
| `session-manager.md` | **Update** | R2 | SQLite keystore, `gateway_id` pinning, boot orphan-guard (§4.6, §5). |
| `store.md` | **Update** | R1 | Ownership `tenant_id`→`organization_id`; fresh v2 migrations (DB reset, §7). |
| `http-foundation.md` | **Update** | R1 | authz middleware (JWKS + api-key), CORS; Authula/cookie middleware removed (§4.3, §4.4). |
| `stream.md` | **Update** | R1 | Stream stays in gateway; auth = JWT *or* api-key; `org` filter (§11). |
| `webhooks.md` | **Update** | R1 | Config org-owned; dispatch/HMAC/retries unchanged (§11). |
| `eventing.md` | **Update** | R1 | Envelope carries `org`; catalog unchanged; auth per §4. |
| `queue.md` | **Update** | R1 | Redis **work** vs **control-bus** roles + key/channel prefixes (§4.6). |
| `inbound-pipeline.md` | **Update (light)** | R1 | Tagging `tenant`→`org`; pipeline logic stable (§9). |
| `outbound-pipeline.md` | **Update (light)** | R1 | Idempotency keyed by `organization_id` (§7, §10). |
| `resources.md` | **Update** | R1 | Resources org-owned; session responses expose `gatewayId` (§13). |
| `contacts.md` | **Update (light)** | R3 | Logic stable; ownership via org; frontend reads (§6.2). |
| `frontend.md` | **Replace** | R3 | React Router SPA → TanStack Start + better-auth; org switcher; serverless (§12). |
| `_recon-whatsmeow.md` | **Keep** | — | whatsmeow recon still valid (whatsmeow stays). |
| `_recon-authula.md` | **Obsolete (historical)** | — | Authula removed; retain only as history — do **not** follow. |

> When you finish rewriting a spec, remove its banner and flip its row here to "✅ v2".
