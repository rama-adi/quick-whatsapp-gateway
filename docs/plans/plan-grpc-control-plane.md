# Plan: gRPC Control Plane and Database-Independent Gateways

Status: **proposed** — migration design of record; implementation will proceed on this branch.
Branch: `migration/grpc-control-plane`.

This plan replaces the current router → gateway HTTP reverse-proxy architecture with an API
control plane and private WhatsApp engine gateways connected through gRPC. It also introduces a
separate public gRPC surface alongside the existing REST/OpenAPI API.

The migration is intentionally incremental: every checkpoint must keep the repository buildable,
testable, and deployable. The HTTP proxy, gateway MySQL access, and gateway Redis access are removed
only after their replacements are operating and verified.

---

## 1. Goals

1. Make the API the only public front door for REST, public gRPC, WebSocket realtime, and API
   documentation.
2. Move public request/response definitions, Huma operations, validation, authorization, and
   OpenAPI generation into the API process.
3. Replace API → gateway HTTP proxying with a private, versioned gRPC engine contract.
4. Remove MySQL and Redis as gateway runtime requirements.
5. Keep whatsmeow cryptographic state durable and gateway-local in SQLite.
6. Provide reliable, idempotent API → gateway commands and acknowledged gateway → API events.
7. Authenticate API ↔ gateway communication with per-gateway mTLS identities, including secure
   enrollment, automatic certificate rotation, disablement, and recovery.
8. Maintain persistent HTTP/2 connections so normal gateway commands do not pay a connection setup
   cost.
9. Support multiple gateways, deterministic session placement, draining, and desired-state
   reconciliation without gateways reading the control-plane database.
10. Expose an optional public gRPC API without allowing public callers to reach the gateway engine
    contract.
11. Make the authenticated web admin interface the primary workflow for creating, enrolling,
    observing, draining, disabling, and recovering gateways.

## 2. Non-goals

- Removing the gateway-local whatsmeow SQLite keystore.
- Making a session instantly portable between gateways. Moving the cryptographic keystore remains
  a separate backup/restore or shared-keystore problem.
- Building automated keystore snapshot upload, cross-gateway rehydration, or transparent failover
  in this migration. The design leaves a clean seam for it, but does not claim it yet.
- Making gateways stateless. They remain stateful WhatsApp engines with local device keys and a
  small durable event journal.
- Tunnelling public gRPC directly to a gateway.
- Replacing the browser's REST and WebSocket clients with native gRPC. Browsers continue to use
  REST/WebSocket unless gRPC-Web or Connect is deliberately added later.
- Preserving the old internal HTTP gateway API after cutover. The codebase is pre-release, so the
  final state has one internal transport.

---

## 3. Target architecture

```text
 Browser / SDK / integration
      | REST + JSON       public gRPC       WebSocket
      +------------------------+------------------+
                               v
                    API / control plane
                    - public authn + authz
                    - Huma REST + OpenAPI
                    - public gRPC adapters
                    - application services
                    - MySQL repositories
                    - Redis jobs/realtime/cache
                    - placement + desired state
                    - webhook delivery
                               |
                  private gRPC over mTLS/HTTP2
                               |
             +-----------------+-----------------+
             v                 v                 v
         gateway A         gateway B         gateway C
         whatsmeow         whatsmeow         whatsmeow
         SQLite keys       SQLite keys       SQLite keys
         event journal     event journal     event journal
```

The API is the control plane and system of record. A gateway is a private data-plane process that
owns only live WhatsApp connections, cryptographic device state, transient runtime state, and the
durable handoff journal required to survive an API outage.

### Runtime dependency boundary

| Dependency | API | Gateway |
|---|---:|---:|
| MySQL | required | no |
| Redis | required | no |
| whatsmeow SQLite | no | required |
| local event journal | no | required |
| public TLS/HTTP | required | no |
| private gRPC/mTLS | required | required |
| WhatsApp network | no | required |

“Database-independent gateway” means no shared application database. SQLite remains mandatory
because losing whatsmeow Signal sessions and device keys forces every number to pair again.

---

## 4. Responsibility split

### API owns

- Public REST handlers, DTOs, validation, error mapping, Huma operations, and OpenAPI generation.
- Public gRPC service implementations and protobuf compatibility policy.
- Better Auth JWT/API-key authentication and all user authorization.
- Organization isolation, capability gates, session ownership, and gateway placement.
- MySQL migrations and all WA application-data repositories.
- Outbound command records, idempotency, scheduling, retry policy, and product rate limits.
- Inbound event ingestion, deduplication, transactions, history, and projections.
- Webhook configuration, enqueueing, signing, retry, and delivery.
- Redis-backed jobs, OAuth pending state, poll recap scheduling, control events, and realtime fan-out.
- Gateway registry, certificate enrollment records, desired state, configuration revisions, drain
  state, and observability.

### Gateway owns

- whatsmeow clients and their connection lifecycle.
- Pairing, logout, device removal, and live session state.
- Sending WhatsApp messages and executing live presence/contact/group/channel operations.
- Receiving and normalizing whatsmeow events into versioned internal event messages.
- Gateway-local whatsmeow SQLite and its backup/restore hooks.
- A durable outbound event journal with retry and acknowledgement handling.
- A small in-memory defensive limiter to protect WhatsApp from accidental bursts. Product/account
  rate limits remain API-owned.
- Desired-state reconciliation for sessions assigned by the API.

The gateway never receives an end-user bearer token, API key, role, or permission set. The API
authorizes a user and then issues a service command to the assigned engine.

---

## 5. Contracts and package layout

Keep the public and private contracts separate even where their fields look similar.

```text
proto/
  public/v1/
    sessions.proto
    messages.proto
    resources.proto
    events.proto
  gateway/v1/
    enrollment.proto
    control.proto
    engine.proto
    events.proto
```

Proposed Go layout after migration:

```text
cmd/api/                    public API + control-plane composition root
cmd/gateway/                private WhatsApp engine composition root
internal/api/http/          Huma REST adapters
internal/api/grpc/          public gRPC adapters
internal/api/gateway/       private gateway gRPC clients and registry
internal/application/       transport-independent use cases
internal/store/             API-only MySQL repositories
internal/gateway/           gateway engine composition and reconciliation
internal/wa/                whatsmeow-specific runtime code
internal/pki/               enrollment, identity extraction, renewal policy
gen/public/v1/              generated public protobuf Go code
gen/gateway/v1/             generated private protobuf Go code
```

Exact package moves should be made in small steps; the important rule is dependency direction:

```text
REST/public gRPC -> application services -> repositories + GatewayEngine interface
private gRPC client implements GatewayEngine
private gRPC server adapts GatewayEngine RPCs -> whatsmeow runtime
```

REST handlers must not make loopback calls to public gRPC, and public gRPC handlers must not invoke
REST. Both adapt into the same application services.

### Contract tooling

- Add pinned `protoc` generation through Buf.
- Commit generated Go protobuf/grpc code.
- Add lint and breaking-change checks for both protobuf packages.
- Do not expose `gateway/v1` in public SDKs or documentation.
- Keep Huma operations as the REST/OpenAPI source of truth during this migration.
- Add equivalence tests for REST and public gRPC behavior where both expose the same capability.
- Reconsider protobuf-driven REST transcoding only after the control-plane migration is complete.

---

## 6. Private gateway gRPC design

### Connection model

For addressable gateways, the API holds one reusable `grpc.ClientConn` per active gateway. grpc-go
keeps the HTTP/2 connection alive and multiplexes concurrent unary calls; no custom stream is
needed to reduce ordinary command latency.

The gateway also opens a long-lived outbound control/event stream to the API. This stream carries:

- registration and heartbeats;
- capability and version reports;
- local device inventory;
- connection-state transitions;
- inbound messages, receipts, sync events, and failures;
- event acknowledgements;
- desired-state snapshots and deltas;
- drain and reconnect instructions.

If a deployment requires outbound-only gateways, introduce reverse command envelopes on this
stream as an explicit later transport mode. Do not prematurely build custom reverse RPC: it would
need correlation, cancellation, deadlines, fairness, flow control, and reconnect semantics already
provided by ordinary gRPC.

### Initial private services

```protobuf
service GatewayEnrollmentService {
  rpc Enroll(EnrollRequest) returns (EnrollResponse);
}

service GatewayControlService {
  rpc Connect(stream GatewayFrame) returns (stream ControlFrame);
  rpc RenewCertificate(RenewCertificateRequest)
      returns (RenewCertificateResponse);
}

service GatewayEngineService {
  rpc GetSessionState(GetSessionStateRequest) returns (SessionState);
  rpc StartSession(StartSessionRequest) returns (SessionState);
  rpc StopSession(StopSessionRequest) returns (SessionState);
  rpc BeginPairing(BeginPairingRequest) returns (PairingResult);
  rpc LogoutSession(LogoutSessionRequest) returns (SessionState);
  rpc SendMessage(SendMessageRequest) returns (SendMessageResult);
  rpc MarkRead(MarkReadRequest) returns (MarkReadResult);
  rpc SetPresence(SetPresenceRequest) returns (SetPresenceResult);
  // Contact, group, channel, status, and live-sync operations follow by slice.
}
```

Every mutating command includes:

- `command_id`: globally stable idempotency key;
- `session_id` and `organization_id`;
- desired-state/config revision where relevant;
- caller deadline propagated as a gRPC deadline;
- trace/request correlation identifiers in metadata.

Do not put user authorization claims in private RPC messages.

---

## 7. Reliability and consistency

### API → gateway commands

WhatsApp can accept a send even when the gRPC response is lost. Therefore retries must not blindly
execute commands again.

1. API creates a durable command/outbox row with a unique `command_id` and public idempotency key.
2. API resolves the assigned gateway and sends the command.
3. Gateway records or caches execution state keyed by `command_id` before/while executing it.
4. Gateway returns the same terminal result for a repeated `command_id`.
5. API persists the WhatsApp result and completes the outbox row transactionally.
6. Ambiguous outcomes enter reconciliation instead of being reported as a definite failure.

The initial gateway command ledger may live in the same local SQLite database as the event journal,
but not in tables owned by whatsmeow. Retention must be long enough to cover API retry windows.

### Gateway → API events

Redis pub/sub is currently lossy when the subscriber is unavailable. Replace it with an
acknowledged, at-least-once flow:

1. Gateway assigns a stable event ID and appends the normalized event to its local journal.
2. Gateway streams journal entries in order, with bounded in-flight batches.
3. API transactionally deduplicates, persists, and projects the event.
4. API acknowledges the highest safely committed event or explicit event IDs.
5. Gateway compacts acknowledged journal entries.
6. Reconnect resends all unacknowledged entries.

The API must enforce uniqueness on event ID. All event consumers, including webhooks and realtime,
run from API-owned committed events—not directly from an uncommitted gateway stream.

Backpressure policy must be explicit. When the journal reaches its configured disk limit, the
gateway reports degraded health and pauses optional sync work; it must never silently discard core
message and receipt events.

### Desired-state reconciliation

On connection, a gateway reports its local device inventory and last applied configuration
revision. The API replies with the authoritative assignments and versioned per-session config.

The gateway then:

- starts assigned devices that exist locally;
- reports assigned sessions whose local keystore device is missing;
- stops devices no longer assigned to it;
- reports unexpected local devices for explicit recovery, never silently adopts them across orgs;
- applies auto-read, typing, rate-safety, and related settings from the snapshot;
- acknowledges the applied revision.

This replaces gateway reads of `wa_sessions`, `organization`, and `gateways` tables, including the
current boot orphan guard and heartbeat writes.

### Session assignment fencing

Desired state must include a monotonically increasing `assignment_epoch` and a renewable lease for
each session. A gateway may run a session only while it owns the current epoch and its lease is
valid. Commands and emitted events carry that epoch; the API rejects stale epochs.

This is deliberately smaller than keystore portability, but prevents the most dangerous failure:
two gateways operating the same restored or stale session after a partition. Loss of the gateway's
keystore still requires operator recovery or re-pairing until snapshot/restore is implemented.

### Initial keystore safety (easy wins)

Automated remote backup and rehydration are deferred. The migration still adds inexpensive safety
rails around the existing gateway-local SQLite file:

- require an explicit persistent-volume path in production and reject ephemeral/default paths;
- run SQLite `quick_check`/equivalent integrity validation before adopting devices;
- if the API assigns sessions but the expected keystore is absent or empty, report
  `keystore_missing` and stop rather than silently creating replacements or requesting pairing;
- checkpoint WAL and close the store cleanly during graceful drain/shutdown;
- report keystore presence, byte size, integrity status, and last successful check on the control
  stream (without exposing keys or database contents);
- expose metrics and alerts for missing/corrupt storage and persistent-volume pressure;
- keep whatsmeow store access behind the existing consumer interface so a future consistent online
  snapshot implementation does not leak into session management;
- document and test an operator-managed volume backup/restore procedure as a best-effort recovery
  path, without promising automatic failover or a recovery-point objective.

Never copy a live WAL-mode SQLite main file and call it a valid backup. Any future automated backup
must use SQLite's online backup mechanism or a quiesced checkpointed snapshot, encrypt it before
upload, validate it, and restore only under a new fenced assignment epoch.

---

## 8. mTLS identity, enrollment, and rotation

### Identities

- API identity: `spiffe://quick-wa/api`.
- Gateway identity: `spiffe://quick-wa/gateway/{gateway_id}`.
- API derives gateway ID from the verified certificate URI SAN, never from an untrusted message.
- Gateways accept only the configured API identity from the private CA.
- Prefer an offline root CA plus an online intermediate signer. The ordinary API process should not
  hold the root key.

### First enrollment

1. A `super_admin` creates a gateway from the web admin interface. The browser calls the public API;
   it never communicates with the gateway or certificate signer directly.
2. API creates the registry row and returns a random, single-use enrollment token.
3. API stores only the token hash, gateway binding, expiry, and redemption state.
4. Gateway starts with API address, trusted bootstrap CA, gateway ID, and token.
5. Gateway generates its private key locally and submits a CSR over server-authenticated TLS.
6. API atomically consumes the token, validates the requested identity, and signs the CSR.
7. Gateway atomically installs the certificate chain and reconnects using mTLS.
8. API admits the control stream, reconciles desired state, then marks the gateway active.

Enrollment tokens expire within 10–30 minutes, are displayed once, and cannot be used for renewal.
Private keys are never generated by or transmitted to the API.

The API/CLI remains available for automation, but it invokes the same application service and
authorization policy as the web interface. There must not be a separate privileged enrollment path
implemented only in the frontend.

### Web admin onboarding and lifecycle

Add an authenticated gateway administration area under the existing web application:

```text
Admin
  Gateways
    List/detail
    Add gateway
    Enrollment result
    Drain / disable / re-enroll
```

The **Add gateway** form collects only control-plane metadata, initially:

- display name;
- region/environment;
- optional capacity or placement labels;
- optional operator notes.

On submit, the API returns the gateway ID, token expiry, and plaintext enrollment token exactly
once. The result screen provides copyable environment/Docker configuration containing the API
address, gateway ID, token, and trusted CA reference. Navigating away permanently hides the token;
creating a replacement invalidates any previous unredeemed token.

The detail screen reflects server-observed state in near real time:

```text
pending -> connected -> reconciling -> active
                                |-> degraded
active -> draining -> drained
any non-deleted state -> disabled
```

It should show last heartbeat, software/protocol version, capabilities, assigned/live session
counts, applied versus desired revision, certificate expiry, journal pressure, and the last useful
failure reason. Administrative actions are API operations protected by `super_admin` authorization:

- generate a replacement enrollment token for a gateway that never enrolled or lost credentials;
- drain and resume placement;
- disable immediately and terminate its control stream;
- inspect assigned sessions and reconciliation state;
- delete only after the gateway is drained and session/device consequences are acknowledged.

Routine certificate renewal is gateway-driven and needs no button. The UI surfaces renewal health
and expiry; **re-enroll** is the explicit recovery action after credential loss or expiry.

Every gateway creation, enrollment-token issuance/replacement, drain, resume, disable, re-enable,
re-enrollment, and deletion records an audit event containing actor, gateway, action, timestamp,
request ID, and non-secret outcome. Plaintext enrollment tokens, private keys, and CSRs are never
written to logs or audit records.

### Rotation

1. Gateway starts renewal before expiry with jitter.
2. While authenticated by its current certificate, it generates a new key and CSR.
3. API verifies that the CSR requests the same gateway identity and signs it.
4. Gateway atomically installs the new credentials.
5. Gateway establishes a new authenticated connection before draining the old connection.
6. Old certificates expire naturally; disabled gateway identities are rejected immediately by the
   registry and their streams are terminated.

Initial policy: 24-hour leaf lifetime, renewal six hours before expiry, 15–30 minutes of jitter,
and a small `NotBefore` clock-skew allowance. An expired gateway requires a new administrator-issued
enrollment token.

CA rotation uses an overlapping trust bundle: distribute old+new CA, issue new leaf certificates,
then remove the old CA after all valid old leaves have expired.

---

## 9. Public API surfaces

### REST and OpenAPI

- Move Huma operation registration and handlers from the gateway composition root to `cmd/api`.
- The API serves `/api/v1/openapi.yaml` from its generated Huma document.
- `internal/apitypes` and handler input/output structures become API-owned.
- Continue running `make openapi`, `pnpm gen:api`, and `pnpm docs:openapi` for public REST changes.
- Remove all public HTTP and OpenAPI machinery from the final gateway binary.

### Public gRPC

- Expose a separately versioned `public.v1` service surface from the API.
- Authenticate bearer JWTs and API keys through public gRPC interceptors using metadata.
- Resolve the same principal and invoke the same application services as REST.
- Map domain errors consistently to HTTP problem responses and gRPC status codes.
- Support server-streaming events for backend consumers; browsers keep using the API WebSocket.
- Generate SDKs only from `public/v1`, never `gateway/v1`.

Use separate listeners and policies:

```text
API_HTTP_ADDR=:8080
API_PUBLIC_GRPC_ADDR=:8081
API_GATEWAY_GRPC_ADDR=:8443
```

The private listener requires mTLS. Public listeners use public server TLS and public authn.

---

## 10. Redis removal from the gateway

Move each current use before deleting the gateway client:

| Current gateway Redis use | Target |
|---|---|
| `evt:*` event publication | acknowledged gRPC ingest; API publishes realtime after commit |
| per-session product rate limits | API command policy; gateway keeps in-memory safety limit |
| Asynq outbox worker | API |
| retention scheduling/lock | API |
| poll recap sorted-set scheduler | API |
| OAuth/OIDP pending requests and codes | API |
| OIDP app-change subscriber | API-local invalidation/control path |
| control-event publisher | API |
| readiness Redis ping | remove from gateway readiness |

The final gateway configuration has no `REDIS_URL`, `PUBSUB_REDIS_URL`, or `REDIS_PREFIX`.

---

## 11. MySQL removal from the gateway

Move application service orchestration and workers before replacing repository calls. The final
gateway must not import `internal/store`, `internal/dbconn`, the MySQL driver, or migrations.

| Current gateway database use | Replacement |
|---|---|
| session CRUD and ownership | API application service + desired-state push |
| gateway registration/heartbeat | authenticated control stream |
| org existence/orphan guard | API assignment reconciliation |
| message/chat/contact/group persistence | acknowledged event ingestion in API |
| outbox and idempotency | API outbox + local command ledger |
| webhook config/deliveries | API |
| event log | API |
| OAuth apps/grants | API |
| backfill import persistence | API job; live engine work only via explicit RPC if needed |
| DB pool readiness/metrics | API only |

MySQL migrations remain gateway-owned in the repository/tooling sense defined by `AGENTS.md` until
the entrypoint naming is cleaned up: the Go control-plane service is still the sole writer of WA
schema migrations. “Gateway-owned migration” must not be confused with the runtime gateway binary
requiring MySQL.

---

## 12. Migration increments

Each increment should land as a small conventional commit and pass all green gates.

### Increment 0 — lock decisions and tooling

- Update the masterplan and relevant living specs with the new control-plane/data-plane boundary.
- Add Buf configuration, protobuf generation, lint, and breaking checks.
- Add compatibility/version policy and generated-code checks to CI.
- Introduce shared domain/application interfaces without moving runtime behavior.

Exit: empty/private health protobuf can be generated reproducibly; existing system is unchanged.

### Increment 1 — API and gateway composition roots

- Introduce `cmd/api` from the current router and `cmd/gateway` from the current server.
- Keep temporary compatibility entrypoints only within the increment, then update Docker/compose.
- Add separate public HTTP, public gRPC, and private gRPC listener configuration.
- Preserve HTTP reverse proxy while later replacements are absent.

Exit: renamed services deploy with existing behavior and clear dependency ownership.

### Increment 2 — PKI enrollment and gateway control stream

- Add gateway registry enrollment fields and migration.
- Implement the shared gateway-administration application service and public API operations for
  create/list/get, token replacement, drain/resume, disable/re-enable, re-enroll, and safe deletion.
- Implement token creation, CSR enrollment, certificate validation, and identity extraction.
- Implement mTLS control stream, heartbeat, version/capability report, disable, drain, and reconnect.
- Add automatic leaf renewal and zero-downtime connection rollover.
- Add audit events for every privileged gateway lifecycle operation.

Exit: gateway lifecycle no longer depends on self-written registry heartbeats; commands still use
the proxy.

### Increment 2a — web gateway administration

- Add the `super_admin` gateway list, creation, one-time enrollment result, and detail pages.
- Render copyable gateway bootstrap configuration without persisting the plaintext token in browser
  storage, URLs, analytics, or logs.
- Subscribe through the API realtime surface or bounded polling for enrollment/reconciliation state.
- Add confirmation and consequence messaging for drain, disable, re-enroll, and deletion.
- Test route authorization, token one-time visibility, replacement invalidation, and secret leakage.

Exit: a new gateway can be securely created, enrolled, observed, and administered without using a
database console or command-line tool.

### Increment 3 — desired-state session reconciliation

- Define session assignment/config revisions.
- API sends authoritative desired state after gateway connects.
- Gateway reports local device inventory and applies assignment/config changes.
- Replace boot-time org/session MySQL reads and gateway registry writes.
- Add per-session assignment epochs and renewable leases; reject stale commands/events and stop
  sessions whose lease cannot be renewed within the bounded grace period.
- Add production persistent-volume validation, startup SQLite integrity checks, explicit
  `keystore_missing`/`keystore_corrupt` states, graceful WAL checkpointing, and keystore health
  telemetry.
- Document and integration-test the best-effort operator volume backup/restore procedure while
  leaving automated snapshot upload and rehydration deferred.

Exit: gateway can boot and manage existing sessions without reading MySQL for lifecycle metadata.
It cannot silently run a stale assignment or silently replace a missing keystore.

### Increment 4 — first unary engine slices

- Implement low-risk live operations first: session state, presence, read receipts.
- Add API-side `GatewayEngine` client with pooled connections, deadlines, health state, and typed
  error mapping.
- Move corresponding Huma handlers/application services to API ownership.
- Add public gRPC equivalents where useful.

Exit: selected routes execute API application logic and private gRPC without HTTP proxying.

### Increment 5 — reliable gateway event ingestion

- Add gateway-local journal schema and disk/backpressure policy.
- Implement batched streaming, acknowledgement, replay, deduplication, and API transactions.
- Publish WebSocket/public-gRPC events only after commit.
- Move poll recap triggers, webhooks, and projections to API consumers.
- Stop gateway Redis `evt:*` publication after parity soak tests.

Exit: API restarts and network interruptions do not lose or duplicate externally visible events.

### Increment 6 — outbound message commands

- Move outbox scheduling, product rate limits, and retry decisions to API.
- Implement stable command IDs and gateway-local command result ledger.
- Implement `SendMessage` plus ambiguous-result reconciliation.
- Verify media size/streaming strategy; avoid loading large media into unary messages.
- Move remaining outbound workers out of the gateway.

Exit: send retries cannot duplicate a WhatsApp send within the supported idempotency window.

### Increment 7 — remaining live resource operations

- Migrate contact, group, channel, status, sync, pairing, logout, and admin live operations.
- Move reads that are database projections entirely into API services.
- Decide and implement backup-import ownership with explicit resource limits.
- Remove each HTTP gateway route immediately after its gRPC replacement passes parity tests.

Exit: no public operation requires the API reverse proxy.

### Increment 8 — public gRPC API

- Implement `public.v1` adapters over existing application services.
- Add JWT/API-key interceptors, capability enforcement, quotas, reflection policy, and health.
- Add REST↔gRPC behavior/error equivalence tests.
- Generate and document supported public SDKs.
- Add server-streaming events backed by the committed API event service.

Exit: backend consumers can use public gRPC without exposure to gateway identities or contracts.

### Increment 9 — delete gateway MySQL, Redis, and HTTP

- Remove gateway MySQL migrations/opening, repositories, DB readiness, and DB metrics.
- Remove gateway Redis client, queue server/client, pub/sub, and Redis readiness.
- Remove internal assertion/JWKS proxy trust after the last HTTP proxy route is gone.
- Remove gateway Huma/public HTTP handlers and `PUBLIC_URL` proxy registration.
- Restrict gateway deployment networking to private gRPC/operational probes.
- Reduce gateway image and environment configuration.

Exit: gateway starts with only local SQLite, WhatsApp connectivity, and API/mTLS configuration.

### Increment 10 — hardening and operational cutover

- Soak reconnects, API outages, certificate rotation, CA overlap, gateway drain, and disk pressure.
- Add chaos tests for response loss after WhatsApp success and acknowledgement loss after DB commit.
- Add dashboards/alerts for stream lag, journal bytes, oldest unacked event, command ambiguity,
  certificate expiry, reconciliation revision, and gateway clock skew.
- Update all specs, deployment docs, generated API artifacts, and milestone status.
- Delete temporary compatibility names/config and mark the old router plan superseded.

Exit: the target architecture is the only supported deployment path.

---

## 13. Testing and green gates

Existing repository gates remain mandatory:

```sh
go build ./...
go vet ./...
go test ./...
golangci-lint run

cd web
pnpm build
pnpm typecheck
pnpm test
```

Add migration-specific checks:

```sh
buf lint
buf breaking --against <baseline>
buf generate
git diff --exit-code -- gen/
make openapi-check
```

Required integration scenarios:

- enrollment token is single-use, expiring, and bound to one gateway;
- web creation displays the enrollment token once and does not place it in URL, storage, logs, or
  analytics;
- replacing an unredeemed token immediately invalidates the previous token;
- non-`super_admin` users cannot access gateway inventory or lifecycle operations;
- gateway lifecycle actions produce non-secret audit records;
- certificate SAN mismatch and disabled identities are rejected;
- renewal establishes a new stream before closing the old one;
- API restart causes journal replay without duplicate DB rows/webhooks;
- gateway restart replays unacknowledged events and command results;
- send succeeds at WhatsApp but response is lost; retry returns the original result;
- desired-state revision reconnect is deterministic and org-safe;
- stale assignment epochs are fenced after partitions and lease expiry;
- an assigned session with missing/corrupt keystore storage becomes explicitly degraded and is not
  silently recreated;
- graceful shutdown checkpoints/closes the WAL-mode keystore and startup verifies integrity;
- gateway boots with MySQL and Redis unreachable because it no longer uses either;
- REST and public gRPC enforce identical tenant/capability rules;
- private gateway services reject public credentials and non-mTLS callers;
- drain stops placement, completes/aborts bounded in-flight work, and preserves journal entries.

---

## 14. Observability

Correlate one public request through the entire path with:

- public request ID;
- command ID;
- gateway ID;
- session ID;
- organization ID in structured logs only, not unbounded metric labels;
- trace context propagated through gRPC metadata;
- event ID for ingestion and downstream webhook/realtime delivery.

Key metrics:

- active/connecting/draining gateways;
- private gRPC call latency and status by bounded method name;
- control-stream reconnects and last heartbeat age;
- desired/applied configuration revision gap;
- journal entry count, bytes, and oldest unacknowledged age;
- event ingest batch latency and deduplication count;
- command retry, cached-result, and ambiguous-result counts;
- certificate time-to-expiry and renewal failures;
- per-gateway live session count and placement capacity.

Readiness semantics:

- API ready: required DB/Redis available, public listeners healthy, and migration state valid.
- Gateway ready: keystore healthy, valid certificate available, control plane authenticated, desired
  state reconciled, and journal below the critical disk threshold.
- A disconnected gateway may remain live for diagnostics but must be unready for new commands.

---

## 15. Documentation bookkeeping

Behavior changes must update their living spec in the same increment:

- `docs/specs/router.md` becomes API/control-plane architecture and eventually records proxy
  removal.
- `docs/specs/trust-model.md` records public authn/authz plus internal mTLS enrollment/rotation.
- `docs/specs/session-manager.md` records desired-state reconciliation.
- `docs/specs/whatsmeow-store.md` records the retained keystore and local reliability journal.
- `docs/specs/store.md` records API runtime ownership of all shared WA data.
- `docs/specs/eventing.md`, `stream.md`, and pipeline specs record acknowledged ingest.
- `docs/specs/queue.md` records that Redis/asynq are API-only.
- `docs/specs/http-foundation.md` records API-owned Huma/OpenAPI and public gRPC.
- `_V2-STATUS.md`, `masterplan-mvp.md`, and `mvp-progress.md` are updated at milestone changes.
- Any REST shape change follows the full `make gen` bookkeeping path from `AGENTS.md`.

Do not maintain contradictory “old router” and “new API” truths. Update living documents in place as
each migration increment becomes real; this plan describes the intended journey and final state.

---

## 16. Decisions locked by this plan

1. The API is the only public front door.
2. Public REST/OpenAPI and public gRPC terminate at the API.
3. Public and private protobuf contracts are separate packages and compatibility domains.
4. API → addressable gateway commands use normal pooled unary gRPC.
5. Gateway → API lifecycle/events use a long-lived acknowledged stream.
6. Internal service identity uses per-gateway mTLS certificates.
7. End-user authorization is API-only; user credentials never reach gateways.
8. Gateways have no MySQL or Redis runtime dependency.
9. Gateway-local whatsmeow SQLite remains required.
10. Reliable events require a local durable journal and API-side deduplication.
11. Reliable commands require stable command IDs and gateway-side result deduplication.
12. Huma remains the REST/OpenAPI source during migration; protobuf does not silently replace it.
13. Session assignments use epochs and leases to prevent split brain.
14. Automated keystore upload/rehydration is deferred; this migration adds integrity, persistence,
    missing-store, checkpoint, telemetry, and operator-recovery safeguards only.

## 17. Decisions to resolve during Increment 0

- CA implementation for development and production: internal signer, Vault PKI, SPIRE, or managed
  private CA.
- Whether private gateway command endpoints are directly addressable in every supported deployment
  or whether an outbound-only reverse-command mode is required.
- Local journal implementation: separate SQLite file versus application-owned tables beside the
  whatsmeow file. It must never modify whatsmeow-owned tables.
- Future keystore recovery storage and granularity: whole-gateway snapshot versus one store per
  session, and MySQL blob versus encrypted object storage. This does not block the initial safety
  rails.
- Maximum supported offline duration and journal disk budget.
- Command result-ledger retention window.
- Large media transfer: object-storage references, client streaming, or bounded inline payloads.
- Public gRPC SDK languages and protobuf compatibility baseline.
- Whether public server-streaming events ship in the first public gRPC release or follow later.

These decisions affect implementation detail, not the responsibility boundary locked above.
