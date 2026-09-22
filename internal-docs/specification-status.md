# docs/specs — v2 status & refresh tracker

> **Historical design record.** Implementation decisions and milestone checkpoints are preserved below. Current implementation: [architecture overview](../site-marketing/content/docs/architecture/overview.mdx) and [subsystem references](../site-marketing/content/docs/architecture/index.mdx).

Current architecture references live in `site-marketing/content/docs/architecture/` as MDX.
The API owns public authentication, MySQL, Redis, durable event projection, and scheduling.
Gateways own assigned WhatsApp runtimes, local SQLite device credentials and journal, and private mTLS engine RPCs.
The dashboard carries user workflows and public endpoint reference pages; the marketing app carries operator and developer documentation.

The gRPC migration and OAuth/OIDC provider are implemented. Update a subsystem's living MDX contract in the same change as its behavior.

| Spec | v2 disposition | Owning milestone | Notes |
|---|---|---|---|
| `grpc-contracts.mdx` | ✅ Increments 1–10 | gRPC control plane | Public v1 gRPC (health/sessions/messages/streaming events) with shared authn/org/error semantics is live; private engine surface complete; every public operation serves API-locally. **Increment 9:** the gateway binary runs on SQLite keystore + journal + whatsmeow + mTLS gRPC + probes only — MySQL, Redis, and the gateway HTTP API are removed. **Increment 10 (final):** migration complete; chaos semantics pinned by tests. |
| `http-foundation.mdx` | ✅ API-owned Huma + public gRPC | central-router / gRPC Inc 8–9 | Single front door: auth/CORS/control-bus, Huma REST + OpenAPI, ticketed WebSocket, plus the public.v1 gRPC adapters in `backend/internal/apigrpc`. The gateway chi router and admission gate are deleted; only probe endpoints remain on the gateway. |
| `router.mdx` | ✅ Increment 9 | central-router → gRPC control plane | Single front door/trust boundary: router-owned auth/CORS/control bus, REST + OpenAPI, and ticketed WebSocket over shared Redis. The reverse proxy and the Ed25519 internal assertion are removed; live work executes over private mTLS engine RPCs. |
| `trust-model.mdx` | ✅ v2 (replaced `auth-tenancy.mdx`) | R1/R2 + gRPC control plane | Two caller identities (JWKS-JWT, api-key); org ownership; control bus + cache + revocation. Authn + control-bus run on the API; gateways authenticate by per-gateway mTLS identity with strict per-RPC authorization. |
| `api-keys.mdx` | ✅ v2 + central-router | R1/central-router | No custom Go keys; the API verifies against shared `apikey` and owns the positive cache; gateways verify no end-user credentials at all. |
| `whatsmeow-store.mdx` | ✅ Increment 3+9 | R2/gRPC Inc 3+9 | SQLite keystore plus persistent-path validation, integrity health, fail-closed missing/corrupt handling, checkpointed close, and tested operator recovery procedure. It is the gateway's only durable store besides the journal. |
| `session-manager.mdx` | ✅ Increment 7+9 | R2/gRPC Inc 3+7+9 | API-authored assignments/config drive all session startup; the manager holds **no session repository** (wa_sessions is API-owned), boot orphan-guard and admin self-bootstrap are removed, and pairing/lifecycle execute through private engine RPCs. |
| `store.mdx` | ✅ Increment 6+9 | R1/gRPC Inc 3+5+9 | The API is the sole MySQL writer: gateway PKI, administration, atomic assignments/config revisions, reconciliation health/results, fenced event ingest, outbound commands, **and** WhatsApp-data projections derived from committed events. The gateway imports no MySQL code. |
| `http-foundation.mdx` | ✅ v2 + central-router | R1/central-router/Inc 9 | Router (API) owns public JWT/API-key auth, CORS, Huma routes, and generated OpenAPI; the gateway serves only unauthenticated health/readiness/metrics probes. |
| `stream.mdx` | ✅ WebSocket cutover | central-router Increment B | API ticket mint + WebSocket, replay/tail, event filters, and revocation drop are implemented over shared Redis `evt:*`; gateway NDJSON `/events` is removed. |
| `webhooks.mdx` | ✅ v2 + Increment 9 | R1/gRPC Inc 5+9 | Config org-owned; enqueue happens in the committed-event fan-out; the dispatch loop (HMAC/retries) now runs on the API. |
| `eventing.mdx` | ✅ v2 + gRPC Inc 5+9 | R1 + gRPC control plane | Envelope carries `org`; catalog unchanged; auth per §4. Gateway events flow through the local journal to fenced API ingest; a leased post-commit worker projects, then fans out to realtime/webhooks. Recap emission is API-owned. |
| `queue.mdx` | ✅ v2 + Increment 9 | R1/gRPC Inc 9 | Redis work vs control-bus roles unchanged, but both are **API-only**: asynq workers, retention scheduling, rate-limit buckets, pub/sub are off the gateway entirely. |
| `inbound-pipeline.mdx` | ✅ API projection boundary | R1/gRPC Inc 9 | Pipeline stages still classify/enrich locally, but persistence is a no-op (`inbound.NoopRepos`) — chats/messages/polls/votes/receipts/identities/groups are projected API-side from committed events. The architecture reference describes the wired gateway and API stages. |
| `outbound-pipeline.mdx` | ✅ Increment 6+9 | R1/gRPC Inc 6+9 | Durable command rows, product rate limits, retry/backoff, and send projections are API-owned; the gateway executes each command at most once per command id behind its journal ledger. |
| `resources.mdx` | ✅ v2 + gRPC Inc 7 | R1/gRPC Inc 7 | Resources org-owned; session responses expose `gatewayId` (§13). Live group/contact/chat-presence/backfill operations execute API-locally through private engine RPCs with API-owned projections. |
| `contacts.mdx` | ✅ v2 + gRPC Inc 7 | R3/gRPC Inc 7 | Logic stable; ownership via org; frontend reads (§6.2). Live sub-resources execute through private engine RPCs (API-owned projections). |
| `frontend.mdx` | ✅ v2 + central-router | R3/R4/central-router | TanStack Start + better-auth; browsers call the API for actions and ticketed WebSocket realtime. Direct browser→gateway transport is superseded. |
| `backfill-import.mdx` | ✅ implemented | R5 | User-uploaded WhatsApp backup (crypt15) decrypt + SQLite import → chats/messages/identities/groups; once/24h per session (super_admin unlimited). |
| `oauth.mdx` | ✅ implemented | R-OIDC | NEW — "Sign in with WhatsApp": OAuth 2.1 / OIDC provider on the API (`backend/internal/oidp`); DM/group-mention verification via the API committed-event login consumer; two-code pending-auth model in Redis (API-side); consent page + app CRUD in web/. Milestones in [`../../oauth2-progress.md`](oauth2-progress.md). The consumer runs before event projection/fan-out and sends feedback through the API outbound scheduler. |

> All subsystem specs are now v2. The architecture overview maps these subsystem references;
> `../docs/openapi.yaml` is the API contract of record.
>
> **gRPC control-plane migration: COMPLETE (all ten increments landed, Increment 10 ✅).**
> Chaos semantics (lost send response → ledger replay; lost ingest ack → deduplicated
> replay) are pinned by unit tests; temporary compatibility names/config are removed. The
> gRPC-control-plane plan is the historical design record; these specs describe the
> surviving architecture only.
