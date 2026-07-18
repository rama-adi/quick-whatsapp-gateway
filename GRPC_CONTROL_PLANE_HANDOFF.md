# gRPC Control Plane Migration — Work Handoff

**Checkpoint:** 2026-07-19  
**Branch:** `migration/grpc-control-plane`  
**Verified code HEAD:** `4d209fb` (`feat: define fenced gateway control stream contract`)  
**Full design plan:** [`docs/plans/plan-grpc-control-plane.md`](docs/plans/plan-grpc-control-plane.md)

This is the top-level continuation document for another agent. Read the repository `AGENTS.md` first; it is authoritative. The project is pre-release, and the user has explicitly authorized sweeping changes without backward compatibility. Prefer the clean final architecture over transitional compatibility layers.

## Objective

Make the API the only public front door. Move API-to-gateway communication from HTTP reverse proxying to gRPC, expose a public gRPC API alongside REST, and remove MySQL and Redis as gateway runtime requirements. The API owns authentication, authorization, OpenAPI, public request/response types, placement, persistence, and durable coordination. Gateways own WhatsApp connections and local whatsmeow keystores, and communicate with the API through long-lived, mutually authenticated gRPC connections.

The plan deliberately defers durable keystore blob backup/rehydration. Implement only the easy reliability wins listed below for now.

## Completed work

### Increment 0 — foundations

- Buf/protobuf modules, generation, linting, and CI drift checks.
- Transport-independent gateway engine ports.
- Architecture and subsystem documentation updates.
- Baseline Go lint cleanup.

### Increment 1 — composition roots and ownership

- Renamed composition roots to `cmd/api` and `cmd/gateway`.
- Added public API gRPC listener with health service.
- Moved WA schema migrations to the API control plane and retained `cmd/migrate` for operations.
- Updated deployment artifacts and service names for the API/gateway split.

### Increment 2 — implemented portion

- Normalized gateway, enrollment, certificate, PKI, and audit persistence.
- Hardened WA Drizzle introspection to use a reproducible least-privilege MySQL account.
- Added enrollment-token cryptography, opaque CSR handling, SPIFFE validation, and envelope encryption.
- Added persistent local MySQL root/intermediate PKI signer.
- Added crash-safe gateway enrollment application service.
- Added private TLS 1.3 enrollment API listener.
- Anonymous access is limited to the exact enrollment RPC; all other private unary and streaming RPCs require mTLS.
- Added gateway/certificate revocation checks.
- Added crash-safe gateway credential bootstrap, pending CSR reuse, pinned API SPIFFE/root verification, and a persistent mTLS gRPC `ClientConn`.
- Defined the fenced control-stream protobuf contract with versioned/sequence envelopes, hello, heartbeat, and lifecycle frames.

Relevant commits, newest first:

- `4d209fb` — define fenced gateway control stream contract
- `9322296` — bootstrap gateway mTLS credentials
- `d98afeb` — serve gateway enrollment over private TLS
- `5c48e1c` — define private enrollment and API TLS identity
- `2f5c6fe` — add crash-safe gateway enrollment service
- `2b820bd` — add persistent local MySQL PKI signer
- `048217a` — add enrollment token and PKI policy primitives
- `2f020ab` — add gateway enrollment persistence foundation
- `9cb02d1` — align deployment artifacts with API and gateway roots
- `4bf500a` — move schema migrations to API control plane
- `df72e9e` — serve public gRPC health from API
- `887af64` — rename API and gateway composition roots
- `611c9f4` — define transport-independent gateway engine ports
- `ea05122` — establish gRPC control-plane foundation
- `3dca2d7` — clear baseline Go lint findings
- `a91f36d` — add the migration plan

## Current transitional runtime truth

- The API is nominally the front door, but application commands still travel through the transitional API/router-to-gateway HTTP reverse proxy.
- The gateway still requires MySQL and Redis.
- The gateway still serves transitional HTTP handlers and verifies the router's Ed25519 request assertion.
- Public gRPC currently exposes health only; the public application API is not implemented over gRPC.
- Private enrollment and gateway mTLS bootstrap exist and are opt-in.
- The control-stream protobuf contract exists, but no server handler, gateway supervisor, or database fencing runtime is wired.
- The legacy gateway path still self-registers and writes its own heartbeats.
- Desired state, command RPCs, event ingestion, placement cutover, and gateway dependency removal remain unfinished.

## Unfinished work

### Finish Increment 2 — private control plane

- Implement the API-side control-stream service handler.
- Implement the gateway-side reconnecting control-stream supervisor using the existing persistent mTLS connection.
- On connection, authenticate identity exclusively from the TLS peer certificate; never trust a gateway ID supplied in a payload.
- Allocate and persist a monotonically increasing `connection_epoch` for every accepted stream.
- Fence every gateway-originated write in MySQL by `(gateway_id, connection_epoch)` so a stale connection cannot mutate state.
- Enforce envelope version, per-direction monotonic sequence numbers, and replay/out-of-order rejection.
- Implement hello/ack negotiation, heartbeat handling, lifecycle status, graceful disconnect, deadlines, keepalive, jittered exponential backoff, and readiness semantics.
- Move gateway registration, liveness, capabilities, and observed runtime status to the control stream; remove gateway self-registration and direct heartbeat writes.
- Implement certificate renewal over authenticated mTLS, including exact pending-CSR replay behavior.
- Implement certificate and gateway revocation propagation and forced stream termination.
- Add API/admin operations used by the web UI to create/list/view/disable gateways, issue one-time enrollment tokens, and display enrollment commands/status.
- Complete the web gateway-onboarding flow. The browser must never call a gateway directly.
- Add concurrency, replay, stale-epoch, reconnect-storm, renewal, revocation, and crash-boundary tests.

### Increment 3 — desired state and commands

- Define and persist API-owned desired session state and revisions.
- Reconcile desired versus observed state after every gateway reconnect.
- Add private typed command RPCs for session lifecycle, pairing, messaging, contacts, media, and other gateway engine operations.
- Add command IDs, idempotency, bounded deadlines, cancellation, error mapping, and retry classification.
- Ensure the API persists all durable mutations; gateways report results/events through gRPC instead of writing MySQL.
- Establish and document the global lock order before adding concurrent reconcilers and command handlers.

### Increment 4 — events, queues, and streams

- Replace gateway Redis publishing/consumption with gRPC event delivery to the API.
- Add bounded gateway-side buffering and explicit overload/backpressure behavior.
- Persist/process inbound events, webhook jobs, and stream fan-out in the API.
- Decide and implement replay boundaries and deduplication IDs.
- Move webhook delivery and Redis queue/control-bus ownership fully to the API.

### Increment 5 — public REST and gRPC API

- Move all public endpoint input/output DTOs, validation, authorization, and OpenAPI ownership into the API.
- Implement public application gRPC services, not only health.
- Share application services beneath REST and public gRPC transports so behavior and authorization cannot drift.
- Define protobuf-native public errors and map them consistently to REST/OpenAPI errors.
- Regenerate OpenAPI, TypeScript API types, and Fumadocs references whenever REST contracts change.

### Increment 6 — placement and routing cutover

- Route all API application operations through gateway gRPC clients/streams.
- Implement placement, ownership, availability, and failover decisions in the API.
- Return stable unavailable/retry semantics when the owning gateway is offline.
- Remove the HTTP reverse proxy and Ed25519 router-to-gateway assertion after all routes have moved.

### Increment 7 — remove gateway database and Redis dependencies

- Replace every gateway `internal/store`, MySQL, Redis, queue, control-bus, webhook, and stream dependency with control-plane ports.
- Keep only the gateway-local SQLite whatsmeow keystore and narrowly scoped local bootstrap/credential files.
- Remove gateway DB migrations/configuration and prove it starts with neither MySQL nor Redis reachable.
- Keep WA schema ownership and migrations exclusively in the API.

### Increment 8 — resilience and observability

- Add health/readiness distinction, connection-state metrics, structured wide events, tracing propagation, and command/event latency metrics.
- Add bounded queues, overload shedding, retry budgets, circuit breaking where useful, and clean shutdown/drain behavior.
- Test API restart, gateway restart, network partition, duplicate connections, stale connections, MySQL/Redis outages on the API side, and reconnect storms.
- Easy keystore wins only: periodic SQLite checkpointing, atomic local backup, startup integrity checks, corruption detection, and operator-visible recovery state.
- Do **not** implement MySQL keystore-blob backup/rehydration yet; leave it as a later design item.

### Increment 9 — deployment and security hardening

- Finalize separate public REST/gRPC and private mTLS listener exposure in Docker/compose/deployment docs.
- Ensure the private listener is never exposed insecurely; there must be no plaintext/insecure `8443` mode.
- Document firewall, DNS, trust-root distribution, bootstrap token handling, rotation, revocation, and recovery procedures.
- Use asymmetric identities throughout: API-issued client certificates for gateway mTLS and API server certificates pinned to the expected trust root/SPIFFE identity.

### Increment 10 — delete transitional architecture and finish docs

- Delete gateway public HTTP handlers, router reverse proxy, gateway authz/JWKS/API-key verification, internal assertion code, gateway MySQL/Redis wiring, and dead transitional configuration.
- Update the master plan, all affected subsystem specs, deployment guides, onboarding docs, OpenAPI, generated clients, and API reference.
- Run the full repository gates and add an end-to-end proof that the API is the only front door and the gateway runs with only its local keystore plus API connectivity.

## Locked design and safety decisions

- API is the sole public trust boundary and persistence owner.
- Gateway identity comes from mTLS certificate/SPIFFE identity, not request data.
- Enrollment uses a one-time token and CSR; private keys are generated and retained by the gateway.
- Keep a persistent HTTP/2 gRPC connection and long-lived control stream per gateway to reduce handshake latency and enable server-initiated control.
- A new accepted stream receives a new database-backed `connection_epoch`; all old epochs are fenced.
- CSR retry must return the exact prior issuance result for the same pending enrollment, not silently create a new identity.
- Define and obey one global lock order before concurrent runtime work.
- Public REST and public gRPC must call the same application services.
- Separate ports are operational boundaries, not a performance optimization: public REST, public gRPC, and private mTLS gRPC may have different exposure and policy.
- No backward-compatibility layer is required.

## Recommended next resumption sequence

1. Read `AGENTS.md`, this handoff, and the full migration plan.
2. Confirm branch and cleanliness with `git status --short --branch` and inspect recent commits.
3. Inspect the control protobuf and generated service interfaces introduced by `4d209fb`.
4. Implement the API control-stream handler and gateway reconnect supervisor as one bounded increment.
5. Add the `connection_epoch` allocation/fencing transaction before allowing any stream-originated durable mutation.
6. Add focused tests for TLS-derived identity, duplicate streams, stale epoch rejection, sequence replay, reconnect, and shutdown.
7. Update the relevant living specs and progress tracker in the same commit.
8. Continue with certificate renewal/revocation and web-admin onboarding before moving to desired-state commands.

Do not start by exposing more HTTP on the gateway or by adding gateway database conveniences; both move away from the target architecture.

## Key areas to inspect

- `proto/` — public/private protobuf definitions and Buf configuration.
- `gen/` or the configured generated-Go location — generated protobuf/gRPC code.
- `cmd/api/` — API composition root and listeners.
- `cmd/gateway/` — gateway bootstrap and transitional runtime wiring.
- `internal/enrollment/` and related PKI packages — enrollment, certificates, signing, and persistence.
- `internal/router/` — transitional HTTP broker to be replaced and eventually deleted.
- `internal/assertion/` — transitional Ed25519 seam to delete after cutover.
- `internal/wa/` — gateway engine, session manager, and local SQLite keystore.
- `internal/store/`, `internal/queue/`, `internal/controlbus/`, `internal/webhooks/`, `internal/stream/` — functionality that must move out of the gateway runtime.
- `migrations/` — API-owned WA schema.
- `docs/specs/` — living subsystem specifications.
- `web/` — admin onboarding UI and generated REST client/docs.

Use `rg --files` and `rg` to confirm exact package names because the migration is actively reshaping paths.

## Required gates

From the repository root:

```sh
go build ./...
go vet ./...
go test ./...
golangci-lint run
```

From `web/`:

```sh
pnpm build
pnpm typecheck
pnpm test
```

Also run protobuf generation/lint/drift gates defined by the Makefile whenever protobufs change. For public REST changes, edit Go/Huma types and run `make gen` (OpenAPI, TypeScript client, and API docs). For WA schema changes, use API-owned golang-migrate migrations, then run `pnpm db:introspect` with the least-privilege `WA_INTROSPECTION_DATABASE_URL`. Never use Drizzle to migrate WA tables, and never use golang-migrate for better-auth tables.

Before every commit, also run `git diff --check` and ensure living specs changed alongside behavior.
