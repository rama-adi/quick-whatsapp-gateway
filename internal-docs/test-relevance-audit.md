# Test relevance audit

Audited all 190 original test files against source `81fd0710e8a7bb78a54e353e5169529c6ab61742` and the API/gateway E2E scenarios. Removed 416 named tests and deleted 43 test files; 526 named tests remain. Table-driven cases are counted with their parent test. Helper-only files have zero named tests.

Shared SQL fixtures from the deleted resource test suite now live in `backend/internal/service/shared_test.go`; this helper introduces no tests.

The decision is behavioral: keep an isolated test only for a concrete failure the real-process suite does not exercise. Security, protocol, crypto, concurrency, and frontend labels alone do not justify retention. The frontend is outside the API/gateway E2E boundary. Removed names and assertions are available in the corresponding Git diff.

Validation: run `make api-e2e` for the complete replacement suite and its checksummed evidence, `go -C backend build ./...`, `go -C backend vet ./...`, `go -C backend test ./...`, and the web build/typecheck/test gates. The operator build-and-test guide documents artifact verification and reconstruction.

| Test file | Removed | Retained | Specific remaining gap or removal disposition |
|---|---:|---:|---|
| `backend/cmd/api/e2e_admin_gateways_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_api_restart_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_async_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_auth_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_concurrency_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_echo_reorder_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_echo_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_event_replay_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_external_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_gateway_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_http_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_infra_test.go` | 0 | 1 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_message_ops_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_oidc_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_public_grpc_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_quotes_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_recovery_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_resources_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_send_faults_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_send_matrix_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_stream_test.go` | 0 | 0 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/e2e_test.go` | 0 | 1 | Real-process E2E scenario or its shared fixture; this is the replacement behavior coverage, not an isolated unit test. |
| `backend/cmd/api/private_grpc_mysql_test.go` | 0 | 1 | Real MySQL-backed mTLS enrollment and renewal must reject disabled gateways and revoked certificates, and a restarted gateway must reuse its installed credential even when given an invalid enrollment token. |
| `backend/cmd/api/private_grpc_test.go` | 3 | 10 | Unverified or policy-invalid gateway certificates must be refused; enrollment/renewal errors must not disclose signer or token secrets; stale control epochs, administrative drain, cancellation and TLS version rules are not all driven by E2E. |
| `backend/cmd/api/server_runner_test.go` | 3 | 6 | Failed third listener binds and serving failures must close prior sockets, while shutdown must await HTTP and gRPC draining and force-stop a hung peer; E2E starts and stops healthy listeners. |
| `backend/cmd/api/startup_test.go` | 0 | 2 | API schema migration must complete before opening the database and must abort open on migration failure; E2E starts from an already prepared schema. |
| `backend/cmd/gateway/architecture_test.go` | 1 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/cmd/gateway/control_runtime_test.go` | 5 | 1 | Certificate expiry can cross the renewal window even when all E2E certificates are fresh. |
| `backend/cmd/gateway/e2e_device_test.go` | 0 | 0 | E2E WhatsApp device simulator used by the gateway process suite; contains helpers, no tests. |
| `backend/cmd/gateway/e2e_process_test.go` | 0 | 1 | E2E child gateway process is required by API process integration scenarios. |
| `backend/cmd/gateway/engine_dispatcher_test.go` | 0 | 1 | Malformed private gateway quote context can expose content from another chat; public E2E quote resolution never sends this payload. |
| `backend/cmd/gateway/journal_events_test.go` | 0 | 1 | A stale gateway fence can silently admit an event before durable journal delivery. |
| `backend/cmd/gateway/keystore_runtime_test.go` | 0 | 5 | Missing and corrupt keystores must fail closed; E2E only boots a healthy seeded keystore. |
| `backend/cmd/gateway/private_engine_test.go` | 0 | 1 | Hostile mTLS peers must be rejected; E2E connects a trusted API peer. |
| `backend/cmd/migrate/main_test.go` | 2 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/api/gateway/control_test.go` | 2 | 18 | Malformed control frames, stale epochs, lost persistence acknowledgements, blocked receive deadlines, and unauthenticated peers are not all injected by running E2E. |
| `backend/internal/api/gateway/engine_client_test.go` | 1 | 2 | Endpoint pool isolation and gRPC error classification are not exercised across multiple gateway endpoints by E2E. |
| `backend/internal/api/gateway/event_consumer_test.go` | 2 | 0 | E2E committed-event replay and outbound echo assert durable projection with one visible event. |
| `backend/internal/apigrpc/apigrpc_equivalence_test.go` | 4 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/application/dependencies_test.go` | 1 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/authz/apikey_cache_test.go` | 0 | 7 | A revoked key must evict from cache despite an in-flight lookup, expiration and delegate failures; E2E authenticates keys without these cache races. |
| `backend/internal/authz/apikey_test.go` | 0 | 3 | Invalid better-auth hash inputs, revoked/wrong-org keys and nil repository must fail closed; E2E only submits ordinary issued keys. |
| `backend/internal/authz/contract_test.go` | 0 | 3 | The better-auth hash, permission shape and EdDSA JWT fixture must stay interoperable with the separate web identity runtime; E2E uses its own test bearer. |
| `backend/internal/authz/cors_test.go` | 0 | 3 | Cross-origin preflight must apply allowlist and disabled/wildcard rules; API E2E requests are same-origin clients without browser CORS enforcement. |
| `backend/internal/authz/gates_test.go` | 0 | 3 | API-key capabilities and org/user role gates must deny forbidden actions and mirror verified org context; E2E covers only selected route and role combinations. |
| `backend/internal/authz/jwt_test.go` | 0 | 6 | JWKS refresh, empty key sets, concurrent fetch and canceled waiters must fail safely; E2E supplies a stable signer and does not rotate keys mid-request. |
| `backend/internal/authz/middleware_test.go` | 0 | 2 | Ambiguous bearer formats, failed token verification and principal context assignment must fail closed; E2E exercises a narrow set of valid and invalid headers. |
| `backend/internal/backup/backup_test.go` | 3 | 5 | Malformed protobuf fields and hostile SQLite paths must fail safely, while wrong crypt15 keys and supported root-key formats must decode correctly; E2E imports a positive synthetic crypt15 archive only. |
| `backend/internal/config/config_test.go` | 0 | 7 | Gateway control-plane fields, strict URL paths, environment precedence and invalid overrides must fail or resolve predictably before boot; E2E uses one valid gateway environment. |
| `backend/internal/config/pki_test.go` | 0 | 1 | Missing or malformed PKI key and TTL settings must stop credential initialization; E2E supplies one valid PKI setup. |
| `backend/internal/config/router_test.go` | 0 | 7 | Overlapping public/private listener addresses and invalid hostnames must be rejected, and JWKS/Redis/private endpoint defaults must bind correctly; E2E uses noncolliding loopback ports. |
| `backend/internal/controlbus/controlbus_test.go` | 0 | 6 | API-key revocation, user ban and membership removal must evict exactly the matching live authorization state; E2E does not inject control bus messages. |
| `backend/internal/crypto/aesgcm_test.go` | 1 | 5 | GCM must randomize nonces and reject tampered, truncated or wrong-key ciphertext and invalid key configuration; E2E does not manipulate encrypted database values. |
| `backend/internal/dbconn/dbconn_test.go` | 0 | 3 | MySQL DSN normalization must enable required driver flags without losing custom options, and malformed Redis/MySQL settings must fail at startup; E2E uses valid connection strings. |
| `backend/internal/dbmigrate/dbmigrate_test.go` | 0 | 3 | A failed migration must close resources and preserve source/database errors; no-change and rollback direction must be handled without a running installation in E2E. |
| `backend/internal/domain/controlplane_test.go` | 2 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/domain/domain_test.go` | 6 | 2 | Concurrent ULID generation must not collide or break monotonic order; E2E issues too few IDs to expose entropy or locking defects. |
| `backend/internal/enrollmenttoken/token_test.go` | 0 | 5 | Token parsing and verification must reject altered/noncanonical secrets and fail on bad entropy/selector input; E2E only redeems normally generated tokens. |
| `backend/internal/gateway/controlclient/client_test.go` | 1 | 5 | Certificate rollover and cancelled enrollment must preserve identity and avoid persisting tokens; E2E uses fresh enrollment. |
| `backend/internal/gateway/controlsupervisor/supervisor_test.go` | 3 | 15 | Protocol sequence, ack, reconnect, lease, and terminal-failure races are not driven by E2E happy control stream. |
| `backend/internal/gateway/desiredstate/control_test.go` | 0 | 1 | A control lease can expire or renew without a gateway restart; E2E does not advance the lease clock. |
| `backend/internal/gateway/desiredstate/reconciler_test.go` | 2 | 9 | Callback ordering, partial-start cleanup, malformed fences, and missing/corrupt local inventory require gateway states E2E does not construct. |
| `backend/internal/gateway/enginegrpc/server_test.go` | 5 | 3 | Malformed private requests, foreign gateway targets, and zero epochs must fail closed; public E2E sends valid gateway protocol payloads. |
| `backend/internal/gateway/journal/commands_test.go` | 0 | 2 | Command result immutability and expiration prune are not checked by E2E replay. |
| `backend/internal/gateway/journal/control_test.go` | 0 | 4 | Removed-assignment and malformed-entry recovery plus private media descriptor containment are not asserted by E2E. |
| `backend/internal/gateway/journal/journal_test.go` | 1 | 6 | Future ack, backpressure, corrupt DB, concurrent append, and abrupt close are absent from E2E restart/replay. |
| `backend/internal/gateway/waadapter/inbound_test.go` | 1 | 4 | Poll-vote option resolution and LID sender/group accounting are absent from inbound E2E fixtures. |
| `backend/internal/gateway/waadapter/outbound_test.go` | 1 | 2 | Missing or unresolvable session context must fail without dispatch; E2E supplies an assigned session. |
| `backend/internal/http/handlers/backup_test.go` | 8 | 0 | E2E backup validation and status exercise running route responses. |
| `backend/internal/http/handlers/chat_test.go` | 12 | 0 | E2E chat lifecycle exercises route and durable state. |
| `backend/internal/http/handlers/contact_test.go` | 8 | 0 | E2E live contacts and groups exercise running route and gateway. |
| `backend/internal/http/handlers/fakes_test.go` | 0 | 0 | Helper-only fakes supported deleted handler unit tests; no independent Test functions. |
| `backend/internal/http/handlers/gateway_admin_ops_test.go` | 4 | 0 | E2E admin gateway lifecycle and enrollment secrets cover running operations. |
| `backend/internal/http/handlers/group_test.go` | 17 | 0 | E2E live contacts and groups cover group operations through the gateway. |
| `backend/internal/http/handlers/media_ops_test.go` | 1 | 0 | E2E media transfer tests access, expiry, and response behavior. |
| `backend/internal/http/handlers/message_test.go` | 7 | 1 | E2E inline media payloads stay below the default 1 MiB body limit; this sends a larger real HTTP request. |
| `backend/internal/http/handlers/misc_test.go` | 6 | 0 | E2E route coverage map asserts remaining public operations and failures. |
| `backend/internal/http/handlers/resources_test.go` | 12 | 0 | E2E resource lifecycle covers sessions, storage, webhooks, chats, and contacts. |
| `backend/internal/http/handlers/session_test.go` | 11 | 0 | E2E session lifecycle and public gRPC pairing cover route behavior. |
| `backend/internal/http/handlers/webhook_test.go` | 7 | 0 | E2E webhook lifecycle plus signed retry verify route behavior. |
| `backend/internal/http/middleware/middleware_test.go` | 7 | 11 | Panic recovery, request-ID bounds, wedged handler cancellation, Redis rate-limit failure, scoped keys, and 503 log privacy are absent from E2E. |
| `backend/internal/httpx/context_test.go` | 3 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/httpx/decode_test.go` | 7 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/httpx/json_test.go` | 1 | 3 | Unexpected infrastructure errors must be masked and wrapped domain errors must keep the right public status; E2E does not inject raw database errors at the HTTP writer. |
| `backend/internal/httpx/pagination_test.go` | 3 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/media/storage_test.go` | 0 | 2 | Storage SSRF/credential rejection and real MySQL+S3 versioned retention, relink, and album retry are beyond E2E media transfer. |
| `backend/internal/oidp/login_interceptor_test.go` | 0 | 5 | Bot mentions and canonical group JIDs must authorize only the intended OAuth command and invalidate cached commands; E2E claims via a direct message only. |
| `backend/internal/oidp/pending_m6_test.go` | 1 | 11 | Concurrent claim/cancel and duplicate delivery must have one winner, preserve expiry and cap wrong attempts; E2E verifies only one normal WhatsApp claim. |
| `backend/internal/oidp/provider_test.go` | 2 | 24 | Malformed authorize/token inputs, browser stream disconnect/caps, PKCE modes, code races, refresh reuse and proxy trust must fail closed; E2E covers a normal signed flow plus selected hostile inputs. |
| `backend/internal/oidp/signer_test.go` | 2 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/pki/api_peer_test.go` | 0 | 1 | The gateway must reject an API mTLS peer with wrong identity, chain or EKU; E2E uses a valid API peer. |
| `backend/internal/pki/apiidentity/manager_test.go` | 0 | 5 | Atomic certificate rotation and recovery must not expose a half-written generation or mismatched key; E2E does not corrupt identity files or race rotation. |
| `backend/internal/pki/gatewayidentity/manager_test.go` | 0 | 4 | Gateway enrollment response tampering and interrupted identity writes must preserve the prior valid certificate; E2E does not damage disk generations. |
| `backend/internal/pki/localmysql/architecture_test.go` | 1 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/pki/localmysql/localmysql_mysql_test.go` | 0 | 1 | A real MySQL authority must enforce active-key and rotation transactions under persistence; E2E uses its own prepared authority path. |
| `backend/internal/pki/localmysql/localmysql_test.go` | 0 | 6 | Authority rows, CSR signing and rollback must reject corrupt keys/certificates and invalid transitions; E2E enrolls one healthy gateway. |
| `backend/internal/pki/pki_test.go` | 0 | 6 | Certificates with forged names, extra extensions, wrong EKU/chain or substituted envelopes must be rejected; E2E does not present all malformed certificate classes. |
| `backend/internal/queue/handlers_test.go` | 1 | 4 | Malformed persisted jobs must skip retry, unowned job types must remain unregistered, and retention cutoff must not prune when disabled; E2E runs healthy queues and does not inspect retention worker routing. |
| `backend/internal/queue/redis_test.go` | 0 | 1 | Malformed Redis URLs and TLS/ACL/database selection must map to the intended queue endpoint; E2E uses one plain local Redis URL. |
| `backend/internal/queue/retention_scheduler_test.go` | 0 | 4 | Multiple schedulers must claim a daily prune once and release a claim after enqueue failure; E2E does not run competing retention replicas. |
| `backend/internal/queue/tasks_test.go` | 3 | 1 | Bad durable-job JSON or missing identifiers must fail before invoking consumers; E2E only enqueues valid tasks. |
| `backend/internal/router/realtime_test.go` | 6 | 0 | E2E realtime ticket scoping, single use, and durable resume run through the router. |
| `backend/internal/router/router_test.go` | 2 | 0 | E2E running API routes and organization authorization supersede mocked router tests. |
| `backend/internal/service/backup_import_test.go` | 1 | 5 | Foreign-org imports must conceal session existence; concurrent/quota decisions must block duplicate or over-budget writes; super-admin bypass must preserve source ownership; identity/group/member rows and counts are outside the E2E synthetic chat/message import. |
| `backend/internal/service/committed_events_test.go` | 2 | 3 | A failed event must block later events in its session but allow unrelated sessions; Redis publish failure must replay after worker restart; a consumed OAuth login command must not fan out to projection, stream or webhook. |
| `backend/internal/service/enrollment_architecture_test.go` | 1 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/service/enrollment_mysql_test.go` | 0 | 1 | MySQL token redemption, lease and certificate persistence must keep one winner under concurrent enrollments; API E2E does not run these database races. |
| `backend/internal/service/enrollment_test.go` | 1 | 11 | Unsafe signing-window settings, typed conflict/credential errors, cancellation, replayed certificates and signer failure release must fail closed; E2E enrolls a healthy gateway. |
| `backend/internal/service/event_projection_test.go` | 4 | 9 | Inbound poll metadata/votes, edit/revoke targets, reactions, pairing-code events without identity, logout replay, missing revoke targets and interactive selections must project or skip correctly; E2E primarily verifies basic inbound message, own echo, receipt and successful pairing. |
| `backend/internal/service/gatewayadmin/actions_test.go` | 3 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/service/gatewayadmin/read_test.go` | 2 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/service/message_recorder_adapter_test.go` | 0 | 1 | Newsletter, broadcast, status and unknown JIDs must classify without merging unlike chat types; E2E exercises direct and group chats only. |
| `backend/internal/service/oauth_app_test.go` | 0 | 5 | OAuth secret hash storage, invalid redirect/command/group inputs, cross-org session binding, deletion with live grants and session-deletion cascade must fail closed; E2E creates and deletes a normal app without active grants at deletion. |
| `backend/internal/service/oidp_event_consumer_test.go` | 0 | 3 | Only inbound login commands should be claimed before ordinary projection and unrelated event types must remain visible; E2E verifies one normal direct-message claim. |
| `backend/internal/service/outbound_ratelimit_test.go` | 0 | 5 | Minute/hour windows, reset, per-session isolation and zero-unlimited policy must enforce intended send budgets; E2E does not exhaust quotas. |
| `backend/internal/service/outbound_scheduler_test.go` | 3 | 4 | Poll metadata/mentions must reach committed events; 429 must stop dispatch; exhaustion and deterministic gateway validation must produce honest terminal states; E2E covers normal poll send and retry recovery. |
| `backend/internal/service/poll_recap_test.go` | 0 | 5 | Vote replacement, hidden voter detail, missing identity and resolved voter names must produce correct recap payloads; E2E sends a poll but does not recap votes. |
| `backend/internal/service/renewal_test.go` | 0 | 2 | Exact renewal replay must skip signing and invalid CSR/incumbent must fail before signing; API E2E does not drive renewal replay or malformed cert input. |
| `backend/internal/service/resources_test.go` | 24 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/service/session_live_test.go` | 7 | 4 | A mismatched gateway state must be rejected; live facade failure must propagate; restart failure must not alter desired state; missing controller must report unavailable; E2E uses a healthy facade. |
| `backend/internal/service/workers_test.go` | 1 | 6 | Independent replicas must race one durable claim, fresh leases must not be stolen, expired leases must recover, canceled waiters must not dispatch, canceled owners must mark failure, and retention must reach persistence; E2E has one normal worker with concurrent HTTP calls. |
| `backend/internal/store/apikey_test.go` | 5 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/store/backfill_import_test.go` | 6 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/store/contact_test.go` | 4 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/store/enrollment_architecture_test.go` | 1 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/store/enrollment_tx_test.go` | 0 | 2 | Enrollment state conflicts and recovery eligibility can fail before E2E happy enrollment. |
| `backend/internal/store/event_log_test.go` | 4 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/store/gateway_admin_tx_test.go` | 0 | 2 | Failed admin transitions and reenrollment revocation must be atomic across SQL failures. |
| `backend/internal/store/gateway_assignment_test.go` | 5 | 3 | Partial reassignment and ineligible targets must not commit broken gateway revisions; E2E uses eligible placement. |
| `backend/internal/store/gateway_control_test.go` | 1 | 2 | Recursive secret redaction and stale-worker enrollment ownership are security failure paths E2E does not inject. |
| `backend/internal/store/gateway_epoch_test.go` | 2 | 9 | Stale control epochs and administrative drain must be fenced across reconnect races absent in E2E. |
| `backend/internal/store/gateway_event_ingest_test.go` | 2 | 4 | Stale event fences, lost dispatcher leases, and private descriptor leakage are distinct from E2E replay scenarios. |
| `backend/internal/store/gateway_reconciliation_test.go` | 2 | 6 | Forged or contradictory gateway inventories and corrupt keystore reports are outside healthy E2E reconciliation. |
| `backend/internal/store/helpers_test.go` | 7 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/store/message_test.go` | 9 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/store/oauth_test.go` | 7 | 2 | Refresh-token family reuse and partial signing-key promotion require failure injection E2E does not perform. |
| `backend/internal/store/outbox_test.go` | 10 | 1 | A failed SQL claim update must roll back the locked page so another sender can retry; E2E does not inject this transaction failure. |
| `backend/internal/store/renewal_tx_test.go` | 0 | 2 | Certificate replay across issuance kinds or expired identity is outside E2E fresh-certificate flow. |
| `backend/internal/store/repos_more_test.go` | 20 | 1 | A failed webhook lease write must roll back the claim; E2E exercises webhook retry but not a database lease failure. |
| `backend/internal/store/retention_test.go` | 0 | 2 | Retention must preserve pending webhook deliveries and stop on a failed SQL batch; E2E does not advance retention windows. |
| `backend/internal/store/session_lifecycle_mysql_test.go` | 0 | 2 | Real MySQL foreign keys, atomic session deletion, lease ownership, and retention are not exercised at these race boundaries by E2E. |
| `backend/internal/store/session_test.go` | 8 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/store/storedb/gateway_control_invariants_test.go` | 1 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/store/storedb/outbox_terminal_test.go` | 0 | 1 | Executes claim predicates against a database to prevent terminal outbox rows from being resent. |
| `backend/internal/store/testutil_test.go` | 0 | 0 | Shared SQL mock fixture used by retained transaction failure tests; contains helpers, no tests. |
| `backend/internal/stream/filter_test.go` | 0 | 2 | A tenant ID containing Redis glob characters must not subscribe across organizations, and invalid event filters must not widen scope; E2E uses canonical IDs and filters. |
| `backend/internal/stream/helpers_test.go` | 0 | 0 | Shared test helper; retained only while referenced by the remaining tests. |
| `backend/internal/stream/publisher_test.go` | 0 | 3 | Live pubsub must publish only to a tenant-scoped channel and reject missing organization scope; E2E checks durable stream replay but not these direct Redis routing failures. |
| `backend/internal/stream/registry_test.go` | 0 | 4 | Revocation must cancel only matching key/user streams, deregister closed streams and avoid lock inversion; E2E does not revoke an active stream mid-flight. |
| `backend/internal/wa/application_adapter_test.go` | 20 | 4 | Stale assignment and missing ledger must fail closed before live operations; terminal versus transient ledger behavior is not asserted by E2E. |
| `backend/internal/wa/command_concurrency_test.go` | 0 | 6 | Ledger read/commit cancellation, owner mismatch, and epoch rollover races are not all driven by E2E concurrency. |
| `backend/internal/wa/events/ignore_test.go` | 0 | 2 | System-message filtering and chat classification include transport types absent in E2E. |
| `backend/internal/wa/events/interactive_test.go` | 0 | 2 | Malformed interactive replies and selection normalization are not in E2E event fixtures. |
| `backend/internal/wa/events/message_test.go` | 0 | 2 | Whatsmeow message subtype and sender identity projections include variants absent in E2E injected messages. |
| `backend/internal/wa/events/normalize_test.go` | 1 | 8 | Real whatsmeow event variants including lifecycle, presence, group, call, and newsletter are not fed through E2E. |
| `backend/internal/wa/inbound/fakes_test.go` | 0 | 0 | Shared inbound fake implementations used by retained error and event pipeline tests; contains helpers, no tests. |
| `backend/internal/wa/inbound/pipeline_test.go` | 11 | 13 | Inbound interception, quote fallback, auto-read ordering, fanout fault handling, and non-text event variants are not covered by E2E. |
| `backend/internal/wa/liveops_test.go` | 3 | 2 | Identity merge precedence and LID-backed membership roles are not asserted by live-contact E2E. |
| `backend/internal/wa/manager_test.go` | 6 | 5 | Terminal reconnect, background-work limits, and QR expiration require runtime state transitions absent from E2E. |
| `backend/internal/wa/outbound/engine_media_test.go` | 1 | 1 | Upstream media download failure mapping is absent from E2E successful media transfer. |
| `backend/internal/wa/outbound/interactive_test.go` | 1 | 1 | Malformed interactive payload constraints are not exercised by E2E valid buttons and list sends. |
| `backend/internal/wa/outbound/sender_test.go` | 22 | 3 | Mixed URL/base64 album sourcing, album validation bounds, and cancellation during pacing are absent from send-matrix E2E. |
| `backend/internal/wa/outbound/waclient_test.go` | 3 | 2 | Own group quote identity fallback and wide-image dimensions are not verified by E2E quote/media fixtures. |
| `backend/internal/wa/session_test.go` | 2 | 4 | WhatsApp reconnect classification and jitter bounds require failures the E2E transport does not simulate. |
| `backend/internal/wa/store/sqlite/sqlite_test.go` | 2 | 4 | Corrupt/missing keystore classification, invalid persistent paths, foreign-key safety, and stopped-volume restore are absent in E2E. |
| `backend/internal/wa/store/store_test.go` | 3 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/internal/webhooks/backoff_test.go` | 4 | 1 | A corrupted very large attempt count can overflow exponential backoff; E2E performs a single retry. |
| `backend/internal/webhooks/dispatcher_test.go` | 0 | 9 | Protocol-header spoofing, no-secret signing, exhausted/deleted endpoints, transport/decrypt failures, and multirow claim behavior are outside E2E signed retry. |
| `backend/internal/webhooks/enqueuer_test.go` | 0 | 4 | Multiple matching subscriptions, terminal dedup, wrong-event repository results, and lookup failure are not driven by E2E one-hook scenarios. |
| `backend/internal/webhooks/fakes_test.go` | 0 | 0 | Helper-only fakes support retained webhook fault and fanout tests; no independent Test functions. |
| `backend/internal/webhooks/hmac_test.go` | 3 | 0 | E2E independently computes HMAC-SHA512 over captured request body and verifies exact signature and identity headers. |
| `backend/internal/webhooks/match_test.go` | 0 | 1 | Event-type wildcard matching includes variants beyond the E2E selected event. |
| `backend/migrations/pki_invariants_test.go` | 1 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/migrations/secret_fields_test.go` | 1 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `backend/proto/gateway/v1/gateway_control_contract_test.go` | 3 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `scripts/selfhost-entrypoint-test.sh` | 0 | 2 | The self-host supervisor must terminate sibling processes and propagate a gateway child failure; API E2E does not execute the deployment entrypoint. |
| `web/app/lib/api/token-provider.test.ts` | 0 | 3 | A pending token mint must not repopulate credentials after sign-out or organization switch; backend E2E cannot observe client cache races. |
| `web/app/lib/db/wa-introspection-grants.test.ts` | 0 | 2 | Generated WA database introspection must reject missing or duplicate read-only grants before schema generation; API E2E never runs the web introspection script. |
| `web/app/lib/db/wa-introspection-patch.test.ts` | 0 | 3 | Changed Drizzle emitter text must fail generation and index order must remain stable; API E2E never regenerates the frontend database model. |
| `web/app/lib/events/cacheBridge.test.ts` | 0 | 19 | Out-of-order, replayed, edited and revoked events must update the browser query cache without duplicate or resurrected messages; API E2E does not run this cache. |
| `web/app/lib/events/frames.test.ts` | 0 | 2 | Malformed realtime frames must not advance the browser replay cursor; API E2E validates server frames but not the client parser. |
| `web/app/lib/query.test.ts` | 0 | 1 | Server requests must receive separate query caches to prevent cross-user data leakage; API E2E does not render the web server. |
| `web/app/routes/-oauth/protocol.test.ts` | 0 | 9 | Browser OAuth code parsing and chunked NDJSON framing must survive split/malformed frames without reading another code; API E2E calls endpoints directly. |
| `web/app/routes/-oauth/scopes.test.ts` | 0 | 4 | OAuth consent must describe scopes accurately in both supported languages and countdown must not expire early; API E2E never renders consent. |
| `web/app/routes/-oauth/wait-client.test.ts` | 0 | 10 | The browser must reconnect after stream EOF, stop after terminal/404/abort, and cancel without credentials; API E2E does not execute the wait client. |
| `web/app/routes/_app/admin/-gateway-reconciliation.test.ts` | 0 | 2 | An unexpected local device must render as unassigned rather than exposing an untrusted session ID; API E2E does not render this admin view. |
| `web/app/routes/_app/admin/-gateway-security.test.ts` | 4 | 0 | Removed: assertions duplicate E2E behavior, mirror implementation, or detect incidental changes. |
| `web/app/routes/_app/user/-oauth/validation.test.ts` | 0 | 15 | The OAuth app form must reject unsafe redirects and duplicate URIs and generate PKCE-only public-client setup; API E2E bypasses the dashboard form. |
| `web/app/routes/_app/user/-webhook-editor.test.ts` | 0 | 3 | The editor must omit blank write-only secrets and reject duplicate or gateway-owned integrity headers; API E2E sends HTTP requests directly. |
| `web/app/routes/_app/user/sessions/$sessionId/chats/-viewer-ui.test.ts` | 0 | 2 | Deleted content must remain hidden across message types and edited flag must come from persisted metadata; API E2E does not render chat messages. |
