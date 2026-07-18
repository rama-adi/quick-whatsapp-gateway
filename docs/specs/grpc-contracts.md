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
