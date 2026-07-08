# docs/specs — v2 status & refresh tracker

These subsystem specs were written for the **v1** single-binary MVP (Go + Authula + embedded
React Router SPA + MySQL keystore). The project pivoted to **v2** (gateway + TanStack
Start/better-auth split — see [`../../masterplan-mvp.md`](../../masterplan-mvp.md)). As of **R5**,
every spec has been rewritten to v2 and reflects the shipped, live-validated implementation.

**Rule (masterplan §17 R0 / §20):** one living spec per subsystem; no spec may describe removed v1
behavior without a *superseded* banner. Each spec is fully rewritten **in the same change** as the
R-milestone that re-implements its subsystem.

| Spec | v2 disposition | Owning milestone | Notes |
|---|---|---|---|
| `router.md` | ✅ Increment A | central-router | NEW — the front door + single trust boundary: end-user authn + control-bus subscriber relocated here from the gateway; REST broker (placement / session-owner / any-active / `503 gateway_unavailable`); Ed25519 request-bound internal assertion to the gateway; registry lifecycle Layer 1. Realtime WS is **pending (Increment B)** — the gateway still serves NDJSON `/events`, proxied by the router. |
| `trust-model.md` | ✅ v2 (replaced `auth-tenancy.md`) | R1/R2 + central-router | Two caller identities (JWKS-JWT, api-key); org ownership; control bus + cache + revocation; boot orphan-guard (§4). **Central-router (Increment A):** authn + control-bus subscriber moved to the router; the gateway now trusts the router's Ed25519 assertion. |
| `api-keys.md` | ✅ v2 | R1 | No custom Go keys; gateway verifies vs shared `apikey` (live schema: `key`=base64url-sha256, `reference_id`=org) (§4.2). |
| `whatsmeow-store.md` | ✅ v2 | R2 | Custom MySQL store retired; SQLite via `sqlstore` on `modernc.org/sqlite` (CGO=0), persistent volume, session pinning (§6.1). |
| `session-manager.md` | ✅ v2 | R2 | SQLite keystore, `gateway_id` pinning, boot orphan-guard (§5). |
| `store.md` | ✅ v2 | R1 | Ownership `tenant_id`→`organization_id`; v2 DDL (`gateways`+`gateway_id`); golang-migrate via the binary; no `wmstore_*` in MySQL (§7). |
| `http-foundation.md` | ✅ v2 | R1 | Two-acceptor authz middleware (JWKS+JWT or api-key), CORS; no Authula/cookie, no `/auth` or `/keys` routes (§4.3, §13). |
| `stream.md` | ✅ v2 | R1 | Stream in gateway; auth = JWT *or* api-key; `org` filter (§11). **Transport move pending (Increment B):** the gateway still serves NDJSON `/events` (now behind the assertion middleware, proxied by the router); the WebSocket cutover that replaces it is not done yet. |
| `webhooks.md` | ✅ v2 | R1 | Config org-owned; dispatch/HMAC/retries unchanged (§11). |
| `eventing.md` | ✅ v2 | R1 | Envelope carries `org`; catalog unchanged; auth per §4. |
| `queue.md` | ✅ v2 | R1 + central-router | Redis **work** vs **control-bus** roles + key/channel prefixes (§4.6). **Central-router (Increment A):** the `ctrl:*` subscriber is the **router** now, not the gateways; one-Redis still the default. |
| `inbound-pipeline.md` | ✅ v2 | R1 | Tagging `tenant`→`org`; pipeline logic stable (§9). |
| `outbound-pipeline.md` | ✅ v2 | R1 | Idempotency keyed by `organization_id` (§7, §10). |
| `resources.md` | ✅ v2 | R1 | Resources org-owned; session responses expose `gatewayId` (§13). |
| `contacts.md` | ✅ v2 | R3 | Logic stable; ownership via org; frontend reads (§6.2). |
| `frontend.md` | ✅ v2 (replaced React Router SPA) | R3/R4 | TanStack Start + better-auth (6 plugins, drizzleAdapter, definePayload, personal-org); browser→gateway direct (CORS+Bearer); org switcher (§12). |
| `backfill-import.md` | ✅ implemented | R5 | User-uploaded WhatsApp backup (crypt15) decrypt + SQLite import → chats/messages/identities/groups; once/24h per session (super_admin unlimited). |
| `oauth.md` | 🚧 in progress | R-OIDC | NEW — "Sign in with WhatsApp": OAuth 2.1 / OIDC provider on the router (`internal/oidp`); DM / group-mention verification via a stage-2 inbound interceptor; two-code pending-auth model in Redis; consent page + app CRUD in web/. Milestones in [`../../oauth2-progress.md`](../../oauth2-progress.md). |

> All subsystem specs are now v2. The masterplan is the overview; these specs are the detail;
> `../openapi.yaml` is the API contract of record.
