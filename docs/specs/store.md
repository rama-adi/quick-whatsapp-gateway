# Store — MySQL app-data repositories (`internal/store`)

Status: implemented (R1).

> **Target migration, not current runtime (gRPC control-plane Increment 0).** Shared WA application
> data becomes API/control-plane runtime state: only the API opens MySQL repositories and performs
> application-data writes. Gateways report lifecycle/events and request writes through private gRPC;
> they will not import `internal/store`, `internal/dbconn`, or a MySQL driver. Repository and migration
> repository write ownership below is still current behavior until those call sites move. Schema
> migration ownership has already moved: the API applies `up` before opening its pool/listeners,
> and `cmd/migrate` provides explicit `up|down`; the gateway never executes migrations.

The app-data persistence layer for the WA-domain plane. Repositories expose `internal/domain`
types and mostly use generated `sqlc` query bindings over `database/sql` internally; OAuth/OIDC
repos use the same plain `database/sql` repo boundary directly because their migration was added
after the sqlc baseline. There is still no ORM and no ORM-owned migrations. The gateway remains a
transitional runtime data writer, while only API/control-plane tooling writes schema; the frontend reads
them read-only via Drizzle ([`frontend.md`](frontend.md) § hybrid reads). Masterplan §6, §7.

## Ownership — `organization_id`, not `tenant_id`

Every owned resource is keyed by **`organization_id`** (a better-auth organization id), reached
through org membership — the v1 `tenant_id` / `tenants` mirror and the custom `api_keys` table are
**gone**. `created_by_user_id` is retained for audit. A personal org is auto-created per user on
signup, so solo use is a one-member org. The gateway authorizes from JWT claims /
api-key `reference_id`, never by joining `member` on the hot path ([`trust-model.md`](trust-model.md)).

## Tables & repos

| Table | Repo | Owning key |
|---|---|---|
| `gateways` | `GatewayRepo` | (registry; lifecycle + routing table — the router reads it) |
| `wa_sessions` | `SessionRepo` | `organization_id` (+ `gateway_id` pin, now authoritative for routing) |
| `webhooks` | `WebhookRepo` | `organization_id` |
| `webhook_deliveries` | `WebhookDeliveryRepo` | via `webhook_id` |
| `whatsapp_identities` | `IdentityRepo` | global (central identity, canonical LID) |
| _(no contacts table)_ | `ContactRepo` | projection over identities + chats(DM) + members, scoped via `session_id` |
| `whatsapp_groups` | `GroupRepo` | global |
| `whatsapp_group_members` | `GroupMemberRepo` | identity↔group pivot (role + `tag`), via `session_id` |
| `chats` | `ChatRepo` | via `session_id` |
| `messages` | `MessageRepo` | via `session_id` (inbound captures **and** the gateway's own sends — see below) |
| `polls` | `PollRepo` | via `session_id` (poll-creation options, so votes resolve to text) |
| `poll_votes` | `PollVoteRepo` | via `session_id` |
| `outbox` | `OutboxRepo` | `organization_id` (idempotency) |
| `event_log` | `EventLogRepo` | `organization_id` |
| `messages` + `event_log` + `webhook_deliveries` | `RetentionRepo` | global maintenance (bounded retention batches) |
| `backfill_imports` | `BackfillImportRepo` | via `session_id` (import job status + once/24h quota) |
| `oauth_clients` | `OAuthClientRepo` | `organization_id` |
| `oauth_grants` | `OAuthGrantRepo` | `organization_id` |
| `oauth_refresh_tokens` | `OAuthRefreshTokenRepo` | `organization_id` |
| `oauth_signing_keys` | `OAuthSigningKeyRepo` | global OIDC keyset |

`apikey` and the other better-auth tables are **not** in this repo set — they are frontend-owned
(Drizzle). `APIKeyRepo` here is **read-only** (`GetByHash`) and used solely by `internal/authz`
to verify keys ([`api-keys.md`](api-keys.md)); it is not a key-management repo.

`Store` (`store.go`) aggregates the repos; `New(db *sql.DB)` builds the set. The generated sqlc
package lives under `internal/store/storedb` and is kept behind the repo boundary; callers should
not import generated DB-shaped rows directly. Org-scoped lists are
`ListByOrg(ctx, organizationID)` (sessions, webhooks); session-scoped tables resolve their owning
org via `wa_sessions`.

OAuth/OIDC provider state is added by `migrations/0007_oidc_provider`: org-owned clients, durable
grants, rotating refresh-token rows, and the shared OIDC signing keyset. The public key material is
listed from `oauth_signing_keys`; private JWKs are AES-GCM encrypted by `internal/oidp` before the
repo persists them.

## v2 DDL highlights (`migrations/0001_init.up.sql`)

The v1 `0001_init` + `0002_wmstore` migrations were **dropped** and replaced by a single fresh v2
`0001_init` (pre-release DB reset, no backfill). Conventions: `utf8mb4`/`utf8mb4_unicode_ci`,
epoch-ms `BIGINT` timestamps, `VARCHAR(64)` ULID PKs (or surrogate `BIGINT UNSIGNED AUTO_INCREMENT`).

```sql
CREATE TABLE gateways (            -- registry + lifecycle; the router reads it to route
  id VARCHAR(64) PRIMARY KEY,      -- = GATEWAY_ID
  label VARCHAR(255) NULL, notes TEXT NULL,
  status ENUM('pending_enrollment','joining','active','draining','drained','degraded','disabled'),
  desired_lifecycle ENUM('run','drain'),
  connection_mode ENUM('legacy','control'),
  creator_kind ENUM('system','user'), created_by_user_id VARCHAR(64) NULL,
  base_url TEXT NULL, grpc_endpoint VARCHAR(512) NULL,
  session_count INT UNSIGNED NOT NULL DEFAULT 0, capacity INT UNSIGNED NULL,
  desired_revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
  applied_revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
  reconciliation_status ENUM('pending','healthy','degraded') NOT NULL DEFAULT 'pending',
  keystore_present TINYINT(1) NULL, keystore_bytes BIGINT NULL,
  keystore_integrity ENUM('healthy','missing','corrupt') NULL,
  keystore_checked_at BIGINT NULL,
  journal_state ENUM('healthy','degraded','paused','critical') NULL,
  journal_entries BIGINT UNSIGNED NULL, journal_bytes BIGINT UNSIGNED NULL,
  software_version VARCHAR(128) NULL, capabilities JSON NULL,
  connection_epoch BIGINT UNSIGNED NOT NULL DEFAULT 0,
  enrolled_at BIGINT NULL, connected_at BIGINT NULL, last_seen_at BIGINT NULL,
  created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
  KEY idx_gateways_status_seen (status, last_seen_at)
);

CREATE TABLE gateway_reconciliation_results ( -- latest complete per-device report
  gateway_id VARCHAR(64) NOT NULL, device_jid VARCHAR(255) NOT NULL,
  session_id VARCHAR(64) NULL, assignment_epoch BIGINT UNSIGNED NOT NULL DEFAULT 0,
  status ENUM('applied','keystore_missing','keystore_corrupt','unexpected_local_device') NOT NULL,
  desired_revision BIGINT UNSIGNED NOT NULL, updated_at BIGINT NOT NULL,
  PRIMARY KEY (gateway_id, device_jid)
);

CREATE TABLE wa_sessions (
  id VARCHAR(64) PRIMARY KEY,
  organization_id    VARCHAR(64) NOT NULL,   -- better-auth org id (owner)
  created_by_user_id VARCHAR(64) NULL,       -- audit
  gateway_id         VARCHAR(64) NOT NULL,   -- which gateway holds this session's keystore
  ... status / wa_jid / is_admin_session / rate_per_min / ...
  KEY idx_sessions_org (organization_id),
  KEY idx_sessions_gateway (gateway_id),
  UNIQUE KEY uq_sessions_jid (wa_jid)
);

CREATE TABLE gateway_session_assignments (
  session_id VARCHAR(64) PRIMARY KEY,
  gateway_id VARCHAR(64) NOT NULL,
  assignment_epoch BIGINT UNSIGNED NOT NULL CHECK (assignment_epoch > 0),
  created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
  FOREIGN KEY (session_id) REFERENCES wa_sessions(id) ON DELETE CASCADE,
  FOREIGN KEY (gateway_id) REFERENCES gateways(id) ON DELETE RESTRICT,
  KEY idx_gateway_session_assignments_gateway (gateway_id, session_id)
);
-- webhooks/webhook_deliveries/whatsapp_*/chats/messages/polls/poll_votes/outbox/event_log follow §7
-- polls (0005): UNIQUE (session_id, poll_message_id); options JSON; selectable_count.
-- poll recap metadata (0006): optional end_time, hide_votes, and recap_emitted_at.
--   It is the canonical source of a poll's option list so incoming votes (option
--   hashes) resolve to text, and the durable guard for one poll.recap event after
--   a timed poll closes.
```

`gateway_session_assignments` is the API's desired-state ownership and split-brain fence for the
private control stream. `wa_sessions.gateway_id` remains during the incremental HTTP migration;
the assignment table is authoritative for streamed assignments. A migration seeds every existing
pinned session at epoch 1. Assignment owner changes must increment `assignment_epoch` and the
gateway's `desired_revision`; configuration changes likewise advance that gateway revision.
The API calculates lease expiry when it emits each snapshot rather than persisting heartbeat churn.
An applied revision is persisted only with matching `gateways.connection_epoch` and
`desired_revision`, so stale streams and stale acknowledgements cannot regress it.
`desired_revision` is the snapshot-wide configuration revision in this first slice: a config change
advances its owning gateway revision and resends all assigned configs; a per-session revision is
therefore intentionally not persisted yet.

- **`organization_id`** replaces v1 `tenant_id` on every owned table; `webhooks`, `event_log`,
  `outbox` carry it directly.
- **`gateways` + `wa_sessions.gateway_id`** are the session-pinning seam
  ([`whatsmeow-store.md`](whatsmeow-store.md), masterplan §4.5); with the central router (Increment A)
  `gateways` is now the live **routing table** the router reads, and `wa_sessions.gateway_id` (already
  `NOT NULL`) is **authoritative for routing** ([`router.md`](router.md), [`session-manager.md`](session-manager.md)).
- **No `wmstore_*` in MySQL.** The whatsmeow keystore is gateway-local SQLite, auto-migrated by
  `sqlstore` — it is no longer part of this migration set.
- better-auth's own tables (`user`/`session`/`apikey`/`organization`/`member`/…) are **not**
  defined here; they are frontend-owned (drizzle-kit). Match `organization_id`/`user_id` lengths
  to better-auth ids (`VARCHAR(64)`).

## Gateway control-plane schema foundation (Increment 2.0)

Pre-release reshaping folded the former `0004_gateways_lifecycle` into `0001_init`; `0004` no
longer exists. The normalized foundation adds:

- `gateway_enrollment_tokens`: SHA-256 digest + safe display prefix only, attempts and state timestamps.
- `pki_authorities`: CA certificate plus encrypted private-key ciphertext, nonce, and key id.
- `gateway_certificates`: public leaf certificates, fingerprints, validity, and revocation.
- reusable `audit_events`: actor/action/resource/outcome plus bounded JSON metadata.

Enrollment redemption is a transaction-owned state machine. The public bearer token contains a
non-secret token id used to lock the row (`SELECT ... WHERE id=? FOR UPDATE`); its SHA-256 digest is
then compared by the application. Beginning redemption installs a random 16-byte nonce, the
32-byte CSR digest, and a bounded lease. Only that nonce+CSR owner may finalize or release it;
expired-lease retries must present the identical CSR, preventing a stale signer from consuming a
new worker's attempt.

Gateways are soft-deleted and excluded from operational queries. Enrollment and certificate
foreign keys use `ON DELETE RESTRICT`, retaining security history; deletion is allowed only after
the gateway is quiescent and has no live token or certificate.

The frontend's reproducible read-only mirror uses pinned Drizzle Kit `0.31.10`, writes generated
schema/relations/metadata under `web/app/lib/db/wa-generated`, and is re-exported by `wa.ts`.
Run canonical `pnpm db:introspect` (which delegates to `db:introspect:wa`) with
`WA_INTROSPECTION_DATABASE_URL` and the explicit allowlist in `web/drizzle.wa.config.ts`; CI uses
`pnpm db:introspect:wa:check` against a freshly migrated database. That allowlist deliberately excludes enrollment tokens,
certificates, PKI authorities, and audit events. It is not an authorization boundary: run it with
a dedicated MySQL account granted `SELECT` only on the listed operational tables (never a wildcard
schema grant), for example `GRANT SELECT ON app.gateways TO wa_reader` plus one grant per allowlisted
table. `webhooks` is also excluded because it contains `hmac_secret`; the web UI already uses the
API's secret-free projection. The API/control-plane migration account remains the sole writer. Using that broad account
for introspection is unsupported: the read-only account also limits `information_schema` visibility
to the allowlisted tables and prevents filtered security-table constraints from entering generation.

PKI rotation has two independent database guards. A generated nullable `active_kind` is populated
only for an active authority and has a unique index, so MySQL cannot commit two active roots or two
active intermediates while allowing one of each. Roots have no parent; intermediates require a
distinct root parent through a composite `(parent_authority_id,parent_kind)` `ON DELETE RESTRICT`
self-FK. `pki_rotation_lock` is one hierarchy-wide singleton locked `FOR UPDATE` by
`Store.RotatePKIAuthority`, ordering root and intermediate changes;
the same transaction locks and verifies the current active authority, marks it retiring, inserts
the successor, and rolls everything back if insertion fails. Repositories intentionally do not
expose a multi-statement rotation helper that could run without this transaction.

Increment 2.1a binds issued certificates to their owning enrollment attempt with a retained
`enrollment_token_id`, canonical `csr_sha256`, and an `issuance_kind='enrollment'` discriminator.
The database check requires enrollment rows to retain a token and renewal rows to have no token.
The unique `(gateway_id, csr_sha256)` retry key supports both exact enrollment replay and future
authenticated renewal without allowing a renewal row to masquerade as a token redemption. The
enrollment service and private enrollment transport now consume this foundation.

Increment 2.1b uses `pki_rotation_lock` to serialize empty-store hierarchy bootstrap and active
intermediate renewal. Both rows are inserted atomically during bootstrap; renewal marks only the
old intermediate retiring and inserts its successor in one transaction. Old rows remain history.
The signer installs its validated cache only after commit, and MySQL receives encrypted PKCS#8 only.
Authority writes are private to the hierarchy transaction adapter; the general Store/sqlc surface
does not expose insert or rotation methods that can bypass the singleton lock.

Increment 2.2 stores `trust_bundle_pem` with each gateway certificate so an idempotent redemption
retry returns byte-for-byte issuance material rather than rebuilding it from current PKI state.
`EnrollmentStore` exposes only complete atomic transitions: create-with-token, replace-live-token,
acquire-lease, finalize-issuance, release-lease, and recover-issuance. Their implementations privately
own the transaction and always lock the gateway row before the token row; no public callback or
row-level enrollment mutation toolkit exists. CA signing remains outside the database transaction
under a bounded lease. The general Store does not publish token or certificate mutation repositories. Cleanup
audits are appended only when the nonce-owned release changed exactly one row; an expired owner is
left for identical-CSR reclaim and produces no false failure audit.
Exact and ambiguous issuance recovery accepts normal post-enrollment lifecycle advancement
(`joining`, `active`, `draining`, `drained`) but requires a nondeleted gateway, consumed matching
token, matching CSR/gateway certificate, and a currently active certificate. Disabled/deleted
gateways are never recovery-eligible.

The renewal persistence foundation similarly exposes transaction-owned prepare and finalize
operations, without yet adding an RPC or gateway scheduler. Both lock the gateway and the exact
presented certificate row, matching certificate id, fingerprint, serial, and gateway, then
revalidate its time window and revocation state. Finalize repeats that authorization after signing
to close the TOCTOU window, inserts the tokenless `issuance_kind='renewal'` row and success audit
atomically, and recovers a uniqueness race by returning the exact persisted gateway+CSR issuance.

Enrollment is wired to the opt-in private TLS listener and crash-safe gateway bootstrap. It is not
yet exposed through an operator administration API or UI. Control-disabled boot self-registration
uses the explicit `creator_kind='system'` default. Future admin creation requires
`creator_kind='user'` and a real creator id; the database check forbids ambiguous or fabricated
user attribution.

The control-stream persistence slice adds database-backed `gateways.connection_epoch`.
`AcceptConnection` atomically compare-and-swap increments the epoch and persists validated Hello
metadata in the same write. That metadata includes the optional authenticated transitional
`http_base_url`; when supplied it updates `gateways.base_url`, and when absent the existing value is
preserved. This keeps fresh control-enabled gateways routable without reintroducing the legacy
unfenced Upsert. It rejects missing, deleted, disabled, pending-enrollment, and unenrolled
gateways. Observed `status` and authoritative `desired_lifecycle` are separate. Accept reads
`desired_lifecycle` for Welcome; Hello cannot choose it. Stream Hello/heartbeat updates observed
status only, so reporting DRAINING/DRAINED during shutdown does not persist a desired drain.
`connection_mode` is also explicit: control accept writes `control`, while legacy self-registration
writes `legacy`. Stream-originated heartbeat, lifecycle, and connection-metadata writes include the gateway
id and current epoch in their update predicate. Accept and heartbeat writes never change
`applied_revision`; heartbeats update liveness, session count, and runtime-derived status only.
Lifecycle reports update only reported lifecycle state—never `session_count`—and cannot select
administrative states such as `disabled` or `pending_enrollment`. `degraded` is a durable
gateway-reported status. Once a newer stream is accepted, writes from the older epoch become no-ops
even if that process has not observed the replacement connection. Stream disconnect clears
`last_seen_at` and `connected_at` only when its gateway id and epoch are still current. It does not
write a terminal database status, and an old stream cannot clear a replacement stream's liveness.
A crash still becomes unreachable through the lease/freshness window. This fences control-plane
registry mutation only; it is not yet a session assignment epoch or a command idempotency ledger.

The API sends `HeartbeatAck` only after `HeartbeatForEpoch` succeeds, so an acknowledgement is
evidence that the liveness/session-count/runtime-state update passed the epoch predicate and was
durably applied. Failure or lease expiry sends no acknowledgement. The gateway supervisor waits for
that acknowledgement before scheduling its next heartbeat.

Existing unfenced `GatewayRepo` lifecycle methods remain active only in control-disabled mode:

- **`Heartbeat`** — touch `last_seen_at` + `session_count` (the 30s gateway loop).
- **`SetStatus`** — write observed runtime lifecycle only (`joining`, `active`, `draining`,
  `drained`, or `degraded`); it never changes administrative intent.
- **`SetDesiredLifecycle`** — the separate administrative mutation for enrolled gateways; accepts
  only `run` or `drain`.
- **`ListActive`** — the `active` gateways (gateway-agnostic routing target).
- **`PickForPlacement`** — choose the least-loaded `active` gateway for a new session
  (`POST /sessions` placement).

When `GATEWAY_CONTROL_PLANE_ADDR` enables the control stream, it is the exclusive gateway-registry
writer. The composition root gates off exactly five legacy mutations: joining registration, active
registration, periodic `Heartbeat`, shutdown `SetStatus(draining)`, and shutdown
`SetStatus(drained)`. `ListActive` and `PickForPlacement` remain control-plane reads. This does not
yet remove other gateway MySQL dependencies, nor does it implement directive-owned graceful
lifecycle transitions.

Router reachability uses `connection_mode`: control rows have a 15-second freshness window matching
their advertised lease; legacy rows retain 90 seconds so their 30-second heartbeat remains viable.
A control-to-legacy registration changes the mode and therefore changes the applicable freshness
window. A fenced disconnect makes a current control row immediately unusable by clearing its
liveness timestamps. Placement additionally requires observed `active`, desired `run`, capacity,
and freshness; a shutdown report cannot accidentally make a desired drain sticky, and an
operator-desired drain cannot receive new placement merely because observed status is active.
The disconnect write is attempted on a detached context bounded to five seconds after stream exit;
the epoch predicate remains the authority that prevents stale cleanup from clearing a replacement.

`SessionRepo` gains **`CountByGateway`** (feeds `session_count` in the heartbeat).
`wa_sessions.gateway_id` is unchanged (already `NOT NULL`) and is now **authoritative for routing**.

## Retention indexes (`migration 0009_retention_indexes`)

The retention worker is a global gateway-maintenance writer, not an
organization-scoped request path. Migration **`0009_retention_indexes`** adds
the index support for its bounded deletes:

- `webhook_deliveries(status, created_at, id)` for terminal delivery history;
- `webhook_deliveries(event_id, status)` to protect events still needed by an
  active webhook retry;
- `messages(timestamp, id)` and `event_log(created_at, id)` for ordered
  delete batches.

No table ownership changes: `RetentionRepo` removes at most a bounded batch per
statement and repeats until the pass is caught up. Its deletion order is
terminal webhook-delivery rows, messages, then event-log rows that have no
`pending` or retryable `failed` webhook delivery. This keeps webhook retries
able to reload their event body while still bounding completed history.

A session pinned to a gateway that is missing / not `active` / has a stale heartbeat is a *stranded*
session: the router returns the new **`gateway_unavailable` (HTTP 503)** domain error rather than
hanging. After running the migration, `cd web && pnpm db:introspect` refreshes the read-only WA
Drizzle models.

Both gateway and router open the shared MySQL database through the common pool
configuration and expose the standard process-local `database/sql` collector.
On a `503`, the canonical request event snapshots max/open/in-use/idle
connections plus cumulative wait count/duration. This distinguishes immediate
request cancellation from pool exhaustion without logging the DSN, SQL text, or
credentials.

## Migrations tooling — API/control-plane ownership

`internal/dbmigrate` owns WA-data schema execution via **golang-migrate** embedded over
`migrations/` (`source/iofs`, `database/mysql`). `cmd/api` applies `up` before opening its normal
MySQL pool or any listener; a failure aborts startup. Operations use `cmd/migrate up|down`, and
`make migrate` invokes `cmd/migrate up`. `cmd/gateway` imports no migration package and exposes no
migration subcommand. The auth plane is migrated separately by drizzle-kit in the frontend.

`sqlc` consumes the same migration SQL as schema input plus named queries in
`internal/store/queries/`; it generates typed query methods in `internal/store/storedb/`.
Regenerate with `make sqlc` after changing store query files or WA migrations. The generated types
are DB-shaped by design; repo methods map nullable values, JSON blobs, generated enums, and
`RowsAffected` / `LastInsertId` results back to the stable `domain` API.

The gateway also has read-only hot-path checks against frontend-owned Better Auth tables
(`apikey`, `organization`). Those tables are still migrated only by the frontend Drizzle
toolchain; `internal/store/sqlc_schema/auth.sql` is a sqlc-only schema stub so the gateway's typed
read queries can compile without making the gateway a writer or migration owner for auth tables.

## Decisions (carried from v1, still apply)

- **Upserts** via `ON DUPLICATE KEY UPDATE` on the natural unique key; capture upserts use
  `COALESCE(VALUES(col), col)` so a sparse later sighting never wipes a known value (e.g. a
  resolved push name survives a later nameless sighting); `chats.last_message_at` only moves
  forward via `GREATEST`.
- **Read-time identity resolution.** Message reads enrich rows from
  `whatsapp_identities` rather than storing display data on the message: a left
  join resolves the sender's `sender_name` (by `sender_lid`), and the service
  layer resolves the body's `@`-mentions to `mentionNames` (one
  `IdentityRepo.NamesForMentions` batch per page, keyed by the mention's user-part
  so it lines up with the `@<number>` token in the body). Both are read-only
  projections — never stored columns — so a later name change is reflected without
  rewriting messages.
- **Chat-list projection.** `ChatRepo.ListBySession` is an inbox view, not an
  address book: it omits chat rows with no `last_message_at`, orders by
  `last_message_at DESC, id DESC`, and resolves display names from
  `whatsapp_groups` for groups or `whatsapp_identities` for DMs before falling
  back to `chats.name`. Contacts that were only found in groups stay in the
  contacts/new-chat flow until a message exists. DM chat reads also expose
  `aliases` from the matched identity (`lid` + linked `phone_jid`) so clients
  can merge rows observed through both WhatsApp address forms.
- **DM alias resolution.** Messages may be captured under either the contact's
  LID or phone JID depending on the event/import source. `MessageRepo.ListByChat`
  expands a DM chat id through `whatsapp_identities` and returns messages stored
  under either alias, so opening either address shows one logical timeline.
  Write paths also canonicalize phone-JID DM chats to the mapped LID when exactly
  one `whatsapp_identities.phone_jid` match exists; identity upserts merge any
  existing phone-JID chat/message/poll rows into the LID row once that mapping is
  discovered. Ambiguous or unknown phone JIDs are left unchanged.
- **Field ownership / no clobber.** Content upserts omit fields with dedicated mutators
  (`messages.status/edited/deleted`, `chats` user flags), so a redelivered capture can't regress a
  receipt. The receipt-specific message mutator is monotonic (`pending` → `sent` →
  `delivered` → `read` → `played`), preserves terminal `failed`, and only increases
  a supplied `ack_level`. Duplicate/stale receipts and unknown message IDs are
  successful no-ops; the latter are expected for pre-capture history and traffic
  from other linked devices.
- **Retention is a gateway-owned maintenance write.** When `RETENTION_DAYS > 0`,
  the gateway schedules one shared-Redis-database, deduplicated daily `retention:prune`
  task. The worker uses the cutoff stored in that task and deletes in bounded
  batches, so it neither locks a large historical range nor competes indefinitely
  with foreground writes. `RETENTION_DAYS=0` disables scheduling (keep forever);
  negative values are invalid configuration. The prune targets are old
  `messages`, `event_log`, and terminal (`delivered` / `dead`) rows in
  `webhook_deliveries`. It intentionally preserves `pending` and retryable
  `failed` delivery rows regardless of age because their payload, attempts, and
  retry time remain operational state, and retains their referenced event-log
  rows until those deliveries are terminal. Event-log retention otherwise bounds
  stream replay: a `since` cursor that is older than the retained history
  replays from the oldest remaining event.
- **Timestamps** are caller-supplied epoch-ms `int64` (`domain.NowMs()`) — repos never call the
  clock, staying deterministic/testable.
- **NULL/JSON.** Nullable columns are `*T`; nullable JSON binds through `nullableJSON`; JSON reads
  as opaque `json.RawMessage` or typed structs (`permissions`, `retry_policy`, `media_meta`,
  `custom_headers`, `events`).
- **`messages` has two writers.** The inbound pipeline writes received messages
  (`direction='in'`, plus `from_me`/`out` rows for sends echoed from the account's
  *other* devices); the outbound pipeline writes the gateway's own sends
  (`from_me=true`, `direction='out'`, `status='sent'`) via
  `MessageRecorderAdapter` on each successful dispatch — see
  [`outbound-pipeline.md`](outbound-pipeline.md). Both go through
  `MessageRepo.Upsert` keyed by `(session_id, wa_message_id)`, so the two paths
  reconcile onto one row rather than duplicating (a self-send and any later echo
  of it collapse to the same message). A **third writer** is the crypt15 backup
  import (`BackupImportService`), which upserts historical messages/chats through
  the same repos — also idempotent by `(session_id, wa_message_id)`, so an import
  merges with live capture (see [`backfill-import.md`](backfill-import.md)).
  Backup imports never key identities or group members by phone JID; unresolved
  phone-only people are skipped until a canonical LID is known.
- **Poll vote idempotency.** `poll_votes` keeps vote history, but WhatsApp event
  replay should not append the same vote twice. The table has a replay key on
  `(session_id, poll_message_id, voter_lid, timestamp)`, and
  `PollVoteRepo.Insert` uses `INSERT IGNORE` so duplicate delivery of the same
  poll-update event is a no-op while later re-votes with a new timestamp remain
  separate history rows. `voter_lid` is the normalized per-voter key supplied by
  the inbound adapter: canonical LID when present, otherwise the sender phone JID.
  It must not be empty for real votes, otherwise same-timestamp votes from
  different voters would share the replay key and recaps could not separate voters.
- **Poll close recaps.** `polls.end_time` stores WhatsApp's poll close time in
  epoch-ms, `hide_votes` mirrors the poll privacy flag, and
  `recap_emitted_at` is the durable exactly-once claim for the synthetic
  `poll.recap` event. Redis holds a best-effort sorted-set timer for low-latency
  wakeups, but MySQL remains authoritative; a periodic sweep over
  `idx_poll_recap_due (end_time, recap_emitted_at)` catches missed Redis entries
  after restarts.
- **`backfill_imports` is the import quota's source of truth.** A user backup
  import is durably tracked here (status + counts + schema fingerprint); the
  once/24h-per-session limit is enforced by `LastSuccessAt` and the concurrency
  guard by `HasRunningSince`, so the quota survives restarts. Owned via
  `session_id`; `super_admin` bypasses the quota.
- **Message ids.** `messages.id` is a generated `msg_<ULID>` string, not an auto-incrementing
  integer. It stays lexicographically sortable for cursor pagination while avoiding a single
  hot monotonic database counter under high write throughput. Message timeline
  pages are returned newest-first; the next cursor is the last row in the page
  and loads older rows with `id < cursor`.
- **Cursor pagination** uses opaque resource-specific cursors
  (`lastMessageAt:id` for the chat inbox, sortable message ids for messages,
  numeric ids elsewhere); limits clamp to `[1,200]` (default 50); bad cursor →
  `validation_error`.
- **Error mapping.** `sql.ErrNoRows` and zero-rows-affected updates/deletes → `domain.ErrNotFound`;
  other DB errors wrapped with `%w` + a `store: <op>` prefix.
- **Concurrency.** `WebhookDeliveryRepo.ClaimDue` selects due rows with `FOR UPDATE SKIP LOCKED`
  and advances `next_retry_at` to a claim lease in the same transaction. Leases are staggered
  by each row's position in the sequential batch, using twice the production HTTP timeout per
  item. Concurrent
  dispatchers therefore receive disjoint batches, while a crashed worker's rows automatically
  become eligible again after the lease. `webhook_deliveries` also has a unique
  `(webhook_id,event_id)` key, making fan-out enqueue idempotent at the database boundary.
  Outbox ownership uses the same database-first rule: `ClaimByID` is a single
  compare-and-set from `queued` (initial attempt), `failed` (retry), or a `sending` row older
  than a caller-supplied lease cutoff to `sending`, incrementing attempts and returning whether
  this process won. Fresh `sending` rows cannot be stolen; stale rows are reclaimable after a
  worker crash instead of remaining stranded forever. `ClaimQueued` locks an ordered page with
  `FOR UPDATE SKIP LOCKED`, applies that CAS to every row, and commits only the complete batch.
  Session-specific batch claims apply their session predicate inside that locking query; they
  never claim another session's row and discard it after the transition.
  Duplicate queue tasks and worker replicas therefore cannot simultaneously own one outbox row;
  a sent/fresh-sending/missing row is a normal non-claim rather than an error.
- **No retained media bytes.** An outbound media send carries either inline base64 in
  `outbox.payload.media.data` or a URL in `outbox.payload.media.url`. `OutboxRepo.UpdateStatus`
  strips `$.media.data` (via `JSON_REMOVE`) when the row is marked `sent`, so inline bytes live
  only until the send is dispatched. URL sends retain only the URL for retry. A `failed` row keeps
  the payload so the async worker can retry.

## How it's tested

`go-sqlmock` (regexp matcher) drives every repo — generated SQL execution, arg binding, row mapping
into `domain` (incl. `*T` nullables + typed JSON), cursor pagination, and `ErrNoRows`/zero-rows →
`not_found` mapping. `CGO_ENABLED=0 go test ./internal/store/...`.
