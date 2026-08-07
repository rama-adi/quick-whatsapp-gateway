# docs/specs — v2 status & refresh tracker

These subsystem specs were written for the **v1** single-binary MVP (Go + Authula + embedded
React Router SPA + MySQL keystore). The project pivoted to **v2** (gateway + TanStack
Start/better-auth split — see [`../plans/masterplan-mvp.md`](../plans/masterplan-mvp.md)). As of **R5**,
every spec has been rewritten to v2 and reflects the shipped, live-validated implementation.

The gRPC control-plane migration is now tracked as **target-state notes alongside current-state
truth**. A target note does not mean its runtime increment has landed; each affected spec labels the
boundary explicitly until the old proxy/database/Redis behavior is removed.

**Rule (masterplan §17 R0 / §20):** one living spec per subsystem; no spec may describe removed v1
behavior without a *superseded* banner. Each spec is fully rewritten **in the same change** as the
R-milestone that re-implements its subsystem.

| Spec | v2 disposition | Owning milestone | Notes |
|---|---|---|---|
| `grpc-contracts.md` | 🚧 Increment 4 active | gRPC control plane | Enrollment, lifecycle, renewal, administration, and revision/epoch/lease-fenced desired-state reconciliation are implemented. Private engine RPC migration is active; reliable events/commands and public gRPC remain. |
| `router.md` | ✅ Increments A+B + Huma | central-router | Single front door/trust boundary: router-owned auth/CORS/control bus, REST broker, Ed25519 assertion, generated Huma OpenAPI, and ticketed WebSocket over shared Redis. Gateway NDJSON is removed. |
| `trust-model.md` | ✅ v2 (replaced `auth-tenancy.md`) | R1/R2 + central-router | Two caller identities (JWKS-JWT, api-key); org ownership; control bus + cache + revocation; boot orphan-guard (§4). **Central-router (Increment A):** authn + control-bus subscriber moved to the router; the gateway now trusts the router's Ed25519 assertion. |
| `api-keys.md` | ✅ v2 + central-router | R1/central-router | No custom Go keys; the router verifies against shared `apikey` and owns the positive cache; gateways receive only the internal assertion. |
| `whatsmeow-store.md` | ✅ Increment 3 | R2/gRPC Inc 3 | SQLite keystore plus persistent-path validation, integrity health, fail-closed missing/corrupt handling, checkpointed close, and tested operator recovery procedure. |
| `session-manager.md` | ✅ Increment 3 | R2/gRPC Inc 3 | API-authored assignments/config, local inventory reconciliation, assignment epochs/leases, and control-mode boot without MySQL lifecycle reads. |
| `store.md` | 🚧 Increment 4 active | R1/gRPC Inc 3 | API owns gateway PKI, administration, atomic assignments/config revisions, durable reconciliation health/results, and placement fencing; engine/event cutover remains. |
| `http-foundation.md` | ✅ v2 + central-router | R1/central-router | Router owns public JWT/API-key auth, CORS, Huma routes, and generated OpenAPI; gateway HTTP trusts only the internal assertion during migration. |
| `stream.md` | ✅ WebSocket cutover | central-router Increment B | Router ticket mint + WebSocket, replay/tail, event filters, and revocation drop are implemented over shared Redis `evt:*`; gateway NDJSON `/events` is removed. |
| `webhooks.md` | ✅ v2 | R1 | Config org-owned; dispatch/HMAC/retries unchanged (§11). |
| `eventing.md` | ✅ v2 | R1 | Envelope carries `org`; catalog unchanged; auth per §4. |
| `queue.md` | ✅ v2 | R1 + central-router | Redis **work** vs **control-bus** roles + key/channel prefixes (§4.6). **Central-router (Increment A):** the `ctrl:*` subscriber is the **router** now, not the gateways; one-Redis still the default. |
| `inbound-pipeline.md` | ✅ v2 | R1 | Tagging `tenant`→`org`; pipeline logic stable (§9). |
| `outbound-pipeline.md` | ✅ v2 | R1 | Idempotency keyed by `organization_id` (§7, §10). |
| `resources.md` | ✅ v2 | R1 | Resources org-owned; session responses expose `gatewayId` (§13). |
| `contacts.md` | ✅ v2 | R3 | Logic stable; ownership via org; frontend reads (§6.2). |
| `frontend.md` | ✅ v2 + central-router | R3/R4/central-router | TanStack Start + better-auth; browsers call the router for actions and ticketed WebSocket realtime. Direct browser→gateway transport is superseded. |
| `backfill-import.md` | ✅ implemented | R5 | User-uploaded WhatsApp backup (crypt15) decrypt + SQLite import → chats/messages/identities/groups; once/24h per session (super_admin unlimited). |
| `oauth.md` | 🚧 in progress | R-OIDC | NEW — "Sign in with WhatsApp": OAuth 2.1 / OIDC provider on the router (`internal/oidp`); DM / group-mention verification via a stage-2 inbound interceptor; two-code pending-auth model in Redis; consent page + app CRUD in web/. Milestones in [`../../oauth2-progress.md`](../../oauth2-progress.md). |

> All subsystem specs are now v2. The masterplan is the overview; these specs are the detail;
> `../openapi.yaml` is the API contract of record.
