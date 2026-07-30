# gRPC contract tooling and compatibility

Status: **Public health and opt-in private enrollment/health listeners active**. The public API
gRPC listener serves only `public.v1.PublicHealthService`; private gateway services
are never registered there and server reflection remains disabled. When `API_GATEWAY_GRPC_ADDR` is
set, a separate TLS 1.3 listener serves `gateway.v1.GatewayEnrollmentService` and
`gateway.v1.GatewayHealthService`.

The root Buf v2 workspace contains two deliberately separate modules and compatibility domains:

- the module rooted at `proto/public` defines the future API-facing gRPC surface under `v1`. Only
  this tree may be used to
  generate public SDKs or public API documentation.
- the module rooted at `proto/gateway` defines the private API-to-gateway engine surface under
  `v1`. It is an internal protocol
  and must never be published as a public SDK.

Both packages start with a health contract so generation and compatibility checks exist before
the engine migration begins. Generated Go and gRPC bindings are committed under matching
`gen/public/v1` and `gen/gateway/v1` directories.

The private module also defines unary `GatewayEnrollmentService.Enroll`. Its request carries only
the one-time enrollment token and DER CSR. Its response returns the resolved gateway id, issued
certificate chain, exact persisted trust bundle, authority/serial identifiers, and millisecond
validity bounds. The adapter bounds inputs before invoking the enrollment service and maps failures
to stable gRPC status codes without returning token, CSR, signer, or database details.

## Generation and checks

Buf CLI `v1.47.2` is invoked through `go run`, so contributors do not need a separately installed
binary. `buf.yaml` declares the two module roots independently; lint and breaking analysis load the
workspace while preserving those boundaries. `buf.gen.yaml` pins the remote `protoc-gen-go` and
`protoc-gen-go-grpc` plugin versions.

```sh
make proto           # regenerate committed Go bindings
make proto-lint      # apply Buf STANDARD lint policy
make proto-check     # lint, regenerate in a temp directory, and reject generated-code drift
make proto-breaking  # compare with origin/main once it contains the initial baseline
```

`PROTO_BREAKING_BRANCH` can override the default `origin/main` comparison ref, and
`PROTO_BREAKING_REF` can override its full Git ref when necessary. The breaking check resolves the
actual remote-tracking ref and intentionally reports a skip while it has no protobuf files. After
this scaffold lands there, one workspace comparison enforces Buf's `FILE` policy across both
modules. `proto-check` does not modify `gen/` or rely on a clean Git worktree: it generates into a
fresh temporary directory and recursively compares that output with the committed bindings.

The GitHub Actions `Protobuf contracts` workflow runs lint, breaking, drift, and generated-package
compile checks for protobuf-related pull requests and pushes to `main`. It fetches full Git history
and the `origin/main` remote-tracking ref so the compatibility baseline is available. These checks
cover the protobuf scaffold only; they do not replace the repository's complete Go and frontend
green gates.

## Version policy

- Package names carry the API major version (`public.v1` and `gateway.v1`).
- Compatible additions stay in `v1`: add new fields with new numbers, new methods, or new messages.
- Never reuse a removed field number or name; reserve both when removing a field.
- Renaming or changing the type/meaning of an existing field, method, service, or enum value is a
  breaking change and requires a new versioned package rather than an exception to the check.
- Generated files are outputs only. Change `.proto` sources and run `make proto`.
- REST/OpenAPI remains Huma code-first; these contracts do not become a second REST source of truth.

The API binds `API_PUBLIC_GRPC_ADDR` (default `:8081`) and registers only
`public.v1.PublicHealthService`. Its status uses the same readiness predicate as HTTP `/readyz`:
`SERVING` only while the API admits traffic and MySQL plus configured Redis are ready, otherwise
`NOT_SERVING`; the RPC itself still completes normally. All public listeners are bound before
either begins serving, and HTTP plus gRPC drain together on cancellation or a serve failure.

This initial listener is plaintext by design for local development or an explicitly configured
trusted path behind a TLS-terminating ingress. It is not a claim that direct production plaintext
gRPC is safe. Production deployments must terminate TLS at the ingress (with a trusted private hop)
or configure application TLS. The public server never registers `gateway.v1` services.

The private listener remains completely disabled while `API_GATEWAY_GRPC_ADDR` is empty.

## Transport-independent engine boundary

Increment 0 also establishes a small consumer-owned boundary in `internal/application`. It contains
only three capabilities already backed by the local WhatsApp runtime: a session-state snapshot,
account presence, and read receipts. `internal/wa.ApplicationGatewayAdapter` satisfies that
boundary over `Manager.ConnectionState` and `LiveOps`, but no composition root constructs it and no
HTTP or gRPC call path uses it yet.

Commands and results carry organization, session, and gateway identifiers. Mutations additionally
carry a stable `command_id` and `assignment_epoch`; the adapter preserves those values but does not
accept epoch zero, claim to deduplicate commands, or validate equality against the current owning
assignment. Ownership/equality protections require the durable command ledger and assignment
authority in later increments. Context carries deadlines, cancellation, and tracing rather than
embedding transport metadata in domain structs.

The presence input is a closed online/offline enum so unknown values cannot silently mean offline.
Read receipts reject empty message IDs, require the participant sender JID for group receipts, and
use a caller-supplied timestamp so retries retain one event time. The existing in-process
read-receipt helper keeps its current `time.Now` behavior. A named 30-second future-clock-skew
allowance is evaluated through an injected clock; old timestamps have no artificial age limit.
These application types import neither Huma nor protobuf/generated bindings, stores, the WA
manager, or outbound implementations.

The enrollment method alone permits a connection with no client certificate. A presented client
certificate must still verify. Every other unary or streaming method—including health and unknown
methods—requires a currently valid, non-revoked certificate for a non-disabled, non-deleted gateway.
Authorization is checked against MySQL on every RPC so revocation takes effect immediately.

The gateway bootstrap client is opt-in through `GATEWAY_CONTROL_PLANE_ADDR`. It pins one canonical
operator root, persists and fsyncs a pending Ed25519 key plus exact CSR before enrollment, and reuses
those bytes across status-aware retries. After validating and atomically publishing the returned
identity it discards the pending state, closes the anonymous connection, and keeps one mTLS gRPC
connection. An already-installed identity does not synchronously require the API to be reachable:
the wired supervisor opens the registered control stream over that connection and reconnects
transient failures with bounded backoff. First enrollment still requires successful token
redemption before the gateway can authenticate.

## Gateway control stream contract and API-side fencing

`gateway.v1.GatewayControlService.Connect` is defined as a private bidirectional stream. Every
gateway/API frame carries
`protocol_version` (field 1), a directional `sequence` (field 2), and exactly one payload. The first
gateway payload is `Hello`; subsequent heartbeats report the connection epoch, last applied control
sequence, timestamp, session count, and bounded runtime state. Lifecycle reports acknowledge one
directive with a bounded failure category. The API answers with `Welcome` (connection identity and
epoch, heartbeat/lease timing, desired lifecycle, server time) and may send epoch-fenced RUN, DRAIN,
or DISABLE directives with an optional drain deadline and bounded reason. A `HeartbeatAck` identifies
the gateway sequence and connection epoch whose heartbeat the API durably persisted; its containing
control sequence is acknowledged by a later gateway heartbeat.

Gateway identity is deliberately absent from every frame: the API handler takes it only from the
authenticated TLS context. Instance IDs identify process incarnations, not gateway
authorization principals. Enum zero values are explicitly `UNKNOWN`; reserved field ranges protect
future compatible additions.

The persistence layer atomically allocates a monotonically increasing `connection_epoch` from MySQL
and stores validated Hello metadata for each accepted connection. Hello may carry an optional
authenticated `http_base_url`, distinct from the future private `grpc_endpoint`; the API validates
it as a canonical absolute HTTP(S) URL and persists it in the same fenced accept write so the
transitional HTTP router can address a freshly enrolled gateway without an unfenced self-Upsert.
Missing, deleted, disabled,
pending-enrollment, and unenrolled gateways cannot allocate an epoch. Heartbeat,
connection-metadata, and the reserved lifecycle persistence primitive require the current epoch;
that lifecycle primitive cannot select administrative states and preserves `session_count`.
Accept and heartbeat preserve `applied_revision`. Runtime health may durably report `degraded`.
A newer accepted stream therefore fences an older stream at the database write boundary.

The private mTLS server registers the API handler. It derives gateway identity exclusively from the
authenticated context. For every inbound frame it validates envelope protocol version and exact
directional sequence before inspecting the payload. Invalid payload fields return
`InvalidArgument`; ordering, acknowledgement, and epoch conflicts return `FailedPrecondition`.
The API requires Hello within ten seconds by default and terminates a registered stream with
`DeadlineExceeded` when no valid heartbeat arrives within its database-derived lease. A successful
current-epoch heartbeat write resets that watchdog and is followed by exactly one sequenced
`HeartbeatAck`; persistence failure sends no acknowledgement.

Welcome derives `desired_lifecycle` from the gateway row's separate authoritative
`desired_lifecycle` field after the accept transaction, never from Hello or from the observed
runtime `status`. The desired value is RUN or DRAIN; a DRAINING/DRAINED heartbeat changes observed
status only and cannot latch an operator desire to drain across the next process start.

Hello requires instance ID and software version lengths of 1–128 bytes, optional gRPC and
transitional HTTP endpoints of at most 512 bytes, a positive representable Unix-millisecond start
time, a known runtime state, and at most 16 unique known capabilities. Heartbeat requires a nonzero
connection epoch, a positive representable Unix-millisecond timestamp, and a known runtime state;
the accepted epoch and Welcome sequence must also match. Stale-epoch writes fail the stream rather
than being accepted as current.

Although the wire contract reserves lifecycle reports for directive acknowledgements and the store
has an epoch-fenced lifecycle primitive, the handler does not persist any lifecycle report yet.
Welcome carries no `directive_id`, so there is nothing a lifecycle report can validly acknowledge.
Until explicit directive tracking exists, every lifecycle report returns `FailedPrecondition`.

The gateway supervisor sends Hello and one heartbeat at a time, validates the complete Welcome/ack
sequence and epoch, and reconnects transient failures with backoff. It becomes ready only after the
API durably acknowledges a heartbeat whose runtime snapshot is READY while authoritative desired
lifecycle is RUN. Disconnect, lease expiry, DRAIN/DISABLE Welcome, and non-READY runtime state keep
readiness false. Control-enabled engine-route admission remains closed until that acknowledged
RUN+READY state. The gate covers every registered engine route, including live GET operations; only
the unauthenticated diagnostics probes remain outside it. A DRAIN/DISABLE Welcome closes admission
terminally for that process: an initial DRAIN skips manager Boot, while a post-Boot DRAIN first
waits for already-admitted requests to leave and then shuts down the manager and its live sessions.

An installed identity may start while the API is transiently unavailable. After a bounded initial
Welcome wait, the diagnostics listener comes up unready with engine admission closed while the
supervisor keeps reconnecting. A later RUN Welcome boots the manager, reports READY, and opens
admission only after the durable acknowledgement. A terminal pre-Welcome authentication or protocol
failure exits clearly instead of leaving a permanently inert process.
The lifetime supervisor result is also part of the composition root's main select: a terminal error
after the diagnostics listener starts terminates the gateway cleanly and is returned as the process
error. The same lifecycle watcher is installed for an immediate or delayed Boot, so a DRAIN arriving
after deferred startup follows the normal admission/worker/manager drain path.

On graceful process shutdown the supervisor has a lifetime independent from the signal-cancelled
application context. Admission closes first; the gateway sends an immediate DRAINING heartbeat and
waits for its durable acknowledgement, shuts down manager work, then sends and waits for a DRAINED
heartbeat before cancelling the stream. Each acknowledgement wait is bounded, so an unavailable
control plane cannot prevent process exit. These are runtime-state heartbeats, not lifecycle
directive reports. `Flush` is generation- and epoch-aware: success requires an acknowledgement
newer than the call, and a reconnect causes the requested runtime snapshot to be reported on the
replacement epoch rather than accepting an old acknowledgement.

Control-enabled mode exclusively owns registry liveness through the stream. It gates off exactly
five legacy direct-MySQL mutations: joining registration, active registration, periodic heartbeat,
shutdown draining, and shutdown drained. Control-disabled mode retains that legacy path.

The API's epoch-fenced disconnect write clears liveness only when the ending stream still owns the
current epoch; a stale stream therefore cannot make its replacement unreachable. The explicit
`connection_mode` written by registration/accept selects freshness: `control` rows use 15 seconds,
matching the advertised lease, while `legacy` rows retain 90 seconds for the 30-second heartbeat
cadence. A later legacy registration switches the row back to legacy freshness.

This is still not full gateway runtime parity. Strict post-Welcome lifecycle directives are
rejected by the supervisor and lifecycle reports remain rejected by the API until issued directives
are tracked; the graceful shutdown heartbeats do not implement that directive/report protocol.
Certificate renewal, revocation-triggered termination, lifecycle reconciliation, and engine
commands also remain unfinished. Disconnect clears liveness but does not invent a terminal
lifecycle status, and a replacement stream fences the old epoch.

Disconnect cleanup runs on a detached context bounded to five seconds. It can therefore clear the
current epoch's liveness after the stream context is cancelled without delaying server teardown
indefinitely.
