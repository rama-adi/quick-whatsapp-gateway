# whatsmeow Gateway — Implementation Spec (v1)

A self-hostable, multi-tenant WhatsApp gateway on **whatsmeow**. Go backend + React Router/shadcn frontend in one monorepo. WAHA-class capability, cleaner API, free — plus a first-class **Contacts** feature (who each account has encountered, and where).

> **Legal / risk notice (ship in README + dashboard footer):** This uses an unofficial WhatsApp client. WhatsApp prohibits bots/unofficial clients; automated use may violate its Terms and get numbers **banned**. Built-in rate limiting and human-mimicry reduce but don't remove the risk. Use at your own risk.

---

## 0. Contents

1. Goals & non-goals · 2. Architecture · 3. Session model · 4. Storage planes · 5. Data model (DDL) · 6. WhatsApp lifecycle, pairing & admin number · 7. Inbound pipeline · 8. Outbound pipeline · 9. Eventing & event schema · 10. Auth, tenancy & API keys (Authula) · 11. REST API (clean design) · 12. Configuration · 13. Frontend (realtime dashboard) · 14. Packaging · 15. Repo layout · 16. Milestones · 17. Deferred · 18. Open micro-decisions · 19. Engineering conventions · 20. Local development

---

## 1. Goals & non-goals

**Goals (v1)**

- Programmatic WhatsApp over whatsmeow for DMs + groups: text, replies, mentions, reactions, edit, delete (revoke), polls (+ vote events), location, contact cards.
- Multi-tenant: users self-register, attach their own number(s), consume events programmatically. User panel toggleable via ENV.
- Admin panel (super-admin) via Authula's Admin plugin; ENV-provisioned admin WhatsApp number that doubles as a normal API-usable number.
- WhatsApp auth via QR or pairing code.
- Two delivery mechanisms: an **HTTP chunked NDJSON stream** (for consumers without a public URL) and **webhooks** (HMAC-signed, retried).
- REST send API + account-global **API keys** with permissions; sessions targeted by path.
- MySQL for messages + a rich **identity/contacts** model; Redis for queue, rate limits, stream fan-out, idempotency, cache.
- Read-only WhatsApp viewer; realtime-ish dashboard.
- Docker compose, two modes: DB-included (local) and external-DB (prod).

**Non-goals (v1) — see §17:** media download/upload (inbound media = metadata only, fire-and-forget; media sends → `501`); horizontal scaling; proxy-per-session; WhatsApp-as-login (`amlogin`, plumbing only); Business labels.

---

## 2. Architecture

Single Go binary; React Router app built to static assets (SPA mode) and embedded/served by Go.

```
WhatsApp <─ws─► Session Manager (N whatsmeow Clients, 1/number, in-process)
                     │ events                         │ send
              Inbound pipeline                  Outbound pipeline
        normalize→capture→store→fanout      ratelimit→ack | async outbox
                     │                              │
   Redis ◄──────────►  Go core (chi router, services, repos)  ◄────► MySQL
 queue/cache/         Auth = Authula (embedded) · API keys · RBAC      app data +
 pubsub/limits        │                              │                 wa keystore
                  Webhook dispatcher           NDJSON stream
                              │
        React Router + shadcn SPA (served by Go): admin · user · viewer
```

HTTP framework: `go-chi/chi`. (Authula also ships a Fiber adapter; swap if preferred — nothing else changes.)

---

## 3. Session model (single-instance v1)

- One `whatsmeow.Client` per attached number, holding a **live WebSocket in-process**. Sessions are stateful; they can't be statelessly load-balanced. **v1 = single application instance** (scale vertically); session-sharding is v2.
- A **Session Manager** owns `map[sessionID]*ManagedSession`, loads devices from the keystore on boot, reconnects with backoff+jitter.
- Statuses (familiar names): `STARTING · SCAN_QR_CODE · WORKING · FAILED · STOPPED · LOGGED_OUT`.
- `LoggedOut` / stream-replaced / ban → mark `LOGGED_OUT`/`FAILED`, **stop** reconnect, emit `session.status`.

---

## 4. Storage planes

Two logical stores; both can live in your one shared MySQL.

| Plane | Contents | Backend |
|---|---|---|
| **App data** | tenants mirror, sessions, messages, identities/contacts, groups, API keys, webhooks, outbox, event log | **MySQL** |
| **whatsmeow keystore** | device identities, Signal sessions, prekeys, sender keys, app-state, LID map | **MySQL (custom store) or SQLite (fallback)** |

**whatsmeow MySQL keystore (the "custom adapter").** whatsmeow's `sqlstore` rides on `go.mau.fi/util/dbutil`, which only defines `Postgres`/`SQLite` dialects and uses `ON CONFLICT` upserts. Don't patch `dbutil`. Implement whatsmeow's `store` interfaces directly against MySQL with plain `database/sql` (`ON DUPLICATE KEY UPDATE`, `?` placeholders, `VARBINARY`/`BLOB`, `utf8mb4`) — a mechanical translation of the existing SQLite schema. Interfaces: `store.DeviceContainer` + per-device `IdentityStore, SessionStore, PreKeyStore, SenderKeyStore, AppStateSyncKeyStore, AppStateStore, ContactStore, ChatSettingsStore, MsgSecretStore, PrivacyTokenStore` + LID map. Build on SQLite first (zero effort), drop in MySQL behind the same interface. Tables namespaced `wmstore_*`.

```
WHATSMEOW_STORE_DRIVER=mysql   # or: sqlite
WHATSMEOW_STORE_DSN=...        # mysql DSN, or file:store.db?_foreign_keys=on
```

---

## 5. Data model (MySQL DDL)

Conventions: `utf8mb4`/`utf8mb4_unicode_ci`; timestamps = **epoch ms** `BIGINT`; surrogate `BIGINT UNSIGNED AUTO_INCREMENT` PKs; tenant isolation via `tenant_id` (global identity/group tables excepted). Roles, bans, and login-sessions live in Authula (§10); `tenants` is a thin app-side mirror keyed by the Authula user id.

```sql
CREATE TABLE tenants (
  id           VARCHAR(64) PRIMARY KEY,            -- = authula user id
  email        VARCHAR(255) NOT NULL,
  display_name VARCHAR(255) NULL,
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL,
  UNIQUE KEY uq_tenants_email (email)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wa_sessions (
  id                VARCHAR(64) PRIMARY KEY,        -- app session id (ULID)
  tenant_id         VARCHAR(64) NOT NULL,
  label             VARCHAR(255) NULL,
  status            ENUM('starting','scan_qr_code','working','failed','stopped','logged_out') NOT NULL DEFAULT 'stopped',
  wa_jid            VARCHAR(255) NULL,              -- phone JID once paired
  wa_lid            VARCHAR(255) NULL,
  phone_number      VARCHAR(64) NULL,
  is_admin_session  TINYINT(1) NOT NULL DEFAULT 0,  -- the WHATSAPP_ADMIN_NUMBER session
  auto_read         TINYINT(1) NOT NULL DEFAULT 1,  -- mark inbound read before acting
  presence_typing   TINYINT(1) NOT NULL DEFAULT 0,
  rate_per_min      INT NOT NULL DEFAULT 20,
  rate_per_hour     INT NOT NULL DEFAULT 200,
  last_connected_at BIGINT NULL,
  created_at        BIGINT NOT NULL,
  updated_at        BIGINT NOT NULL,
  KEY idx_sessions_tenant (tenant_id),
  UNIQUE KEY uq_sessions_jid (wa_jid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Account-global API keys (NOT bound to a session; session targeted by route)
CREATE TABLE api_keys (
  id          VARCHAR(64) PRIMARY KEY,
  tenant_id   VARCHAR(64) NOT NULL,
  name        VARCHAR(255) NOT NULL,
  key_prefix  VARCHAR(16) NOT NULL,                -- shown in UI, e.g. "wak_ab12"
  key_hash    VARCHAR(255) NOT NULL,               -- argon2id of full key
  scope       ENUM('tenant','global') NOT NULL DEFAULT 'tenant', -- 'global' = super_admin only
  permissions JSON NOT NULL,                       -- {"read":bool,"send":bool,"manage":bool,"events":bool}
  last_used_at BIGINT NULL,
  expires_at  BIGINT NULL,
  revoked_at  BIGINT NULL,
  created_at  BIGINT NOT NULL,
  KEY idx_keys_tenant (tenant_id),
  UNIQUE KEY uq_keys_prefix (key_prefix)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE webhooks (
  id            VARCHAR(64) PRIMARY KEY,
  tenant_id     VARCHAR(64) NOT NULL,
  session_id    VARCHAR(64) NULL,                  -- null = all tenant sessions
  url           TEXT NOT NULL,
  events        JSON NOT NULL,                     -- ["message","poll.vote"] or ["*"]
  hmac_secret   VARBINARY(512) NULL,               -- AES-GCM encrypted at rest
  custom_headers JSON NULL,
  retry_policy  JSON NOT NULL,                     -- {"policy":"exponential","delaySeconds":2,"attempts":15}
  active        TINYINT(1) NOT NULL DEFAULT 1,
  created_at    BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL,
  KEY idx_webhooks_tenant (tenant_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE webhook_deliveries (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  webhook_id    VARCHAR(64) NOT NULL,
  event_id      VARCHAR(64) NOT NULL,
  status        ENUM('pending','delivered','failed','dead') NOT NULL DEFAULT 'pending',
  attempts      INT NOT NULL DEFAULT 0,
  response_code INT NULL,
  next_retry_at BIGINT NULL,
  last_error    TEXT NULL,
  created_at    BIGINT NOT NULL,
  KEY idx_deliv_retry (status, next_retry_at)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- ===== Identity / Contacts model =====

-- Global identity resolution (LID -> phone/name). Push name lives here; NO nickname (nickname is group-specific).
CREATE TABLE whatsapp_identities (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  lid           VARCHAR(255) NOT NULL,
  phone_number  VARCHAR(64)  NULL,
  phone_jid     VARCHAR(255) NULL,
  name          TEXT NULL,                          -- push name (preferred display)
  business_name TEXT NULL,
  first_seen_at BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL,
  UNIQUE KEY uq_identity_lid (lid),
  KEY idx_identity_phone (phone_jid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Per-account "found user" record — powers the Contacts feature (where we found them)
CREATE TABLE whatsapp_contacts (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id    VARCHAR(64) NOT NULL,               -- the account that encountered them
  lid           VARCHAR(255) NOT NULL,
  seen_in_dm    TINYINT(1) NOT NULL DEFAULT 0,
  dm_first_seen_at BIGINT NULL,
  dm_last_seen_at  BIGINT NULL,
  message_count BIGINT NOT NULL DEFAULT 0,
  first_seen_at BIGINT NOT NULL,
  last_seen_at  BIGINT NOT NULL,
  UNIQUE KEY uq_contact (session_id, lid),
  KEY idx_contact_lid (lid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE whatsapp_groups (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  group_jid     VARCHAR(255) NOT NULL,
  subject       TEXT NULL,
  description   TEXT NULL,
  owner_jid     VARCHAR(255) NULL,
  participant_count INT NULL,
  is_announce   TINYINT(1) NULL,
  is_locked     TINYINT(1) NULL,
  created_at_wa BIGINT NULL,
  first_seen_at BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL,
  UNIQUE KEY uq_group_jid (group_jid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Pivot: user <-> group. group_nickname is group-specific and lives here.
CREATE TABLE whatsapp_group_members (
  id             BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id     VARCHAR(64) NOT NULL,
  group_jid      VARCHAR(255) NOT NULL,
  lid            VARCHAR(255) NOT NULL,
  group_nickname TEXT NULL,                          -- display name within this group
  role           ENUM('member','admin','superadmin') NOT NULL DEFAULT 'member',
  first_seen_at  BIGINT NOT NULL,
  last_seen_at   BIGINT NOT NULL,
  UNIQUE KEY uq_group_member (session_id, group_jid, lid),
  KEY idx_gm_group (group_jid),
  KEY idx_gm_lid (lid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- ===== Messages & chats =====

CREATE TABLE chats (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id    VARCHAR(64) NOT NULL,
  chat_jid      VARCHAR(255) NOT NULL,
  type          ENUM('dm','group','newsletter','broadcast','status') NOT NULL,
  name          TEXT NULL,
  last_message_at BIGINT NULL,
  unread_count  INT NOT NULL DEFAULT 0,
  archived      TINYINT(1) NOT NULL DEFAULT 0,
  pinned        TINYINT(1) NOT NULL DEFAULT 0,
  muted_until   BIGINT NULL,
  UNIQUE KEY uq_chat (session_id, chat_jid),
  KEY idx_chat_recent (session_id, last_message_at)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE messages (
  id                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id        VARCHAR(64) NOT NULL,
  wa_message_id     VARCHAR(255) NOT NULL,
  chat_jid          VARCHAR(255) NOT NULL,
  sender_lid        VARCHAR(255) NULL,
  sender_jid        VARCHAR(255) NULL,
  from_me           TINYINT(1) NOT NULL DEFAULT 0,
  direction         ENUM('in','out') NOT NULL,
  type              VARCHAR(32) NOT NULL,           -- text,poll,location,contact,reaction,system,image,...
  body              MEDIUMTEXT NULL,
  quoted_message_id VARCHAR(255) NULL,
  mentions          JSON NULL,
  has_media         TINYINT(1) NOT NULL DEFAULT 0,
  media_meta        JSON NULL,                       -- {mimetype,size,filename}; NOT downloaded in v1
  status            ENUM('pending','sent','delivered','read','played','failed') NULL,
  ack_level         INT NULL,
  error             TEXT NULL,
  edited            TINYINT(1) NOT NULL DEFAULT 0,
  deleted           TINYINT(1) NOT NULL DEFAULT 0,
  timestamp         BIGINT NOT NULL,
  raw_json          JSON NULL,                        -- normalized event payload
  created_at        BIGINT NOT NULL,
  UNIQUE KEY uq_msg (session_id, wa_message_id),
  KEY idx_msg_chat (session_id, chat_jid, timestamp),
  KEY idx_msg_sender (sender_lid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE poll_votes (
  id               BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id       VARCHAR(64) NOT NULL,
  poll_message_id  VARCHAR(255) NOT NULL,
  voter_lid        VARCHAR(255) NOT NULL,
  selected_options JSON NOT NULL,
  timestamp        BIGINT NOT NULL,
  raw_json         JSON NULL,
  KEY idx_pollvote (session_id, poll_message_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE outbox (
  id              VARCHAR(64) PRIMARY KEY,            -- ULID
  tenant_id       VARCHAR(64) NOT NULL,
  session_id      VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(255) NULL,
  payload         JSON NOT NULL,
  status          ENUM('queued','sending','sent','failed') NOT NULL DEFAULT 'queued',
  attempts        INT NOT NULL DEFAULT 0,
  wa_message_id   VARCHAR(255) NULL,
  error           TEXT NULL,
  created_at      BIGINT NOT NULL,
  updated_at      BIGINT NOT NULL,
  UNIQUE KEY uq_idem (tenant_id, idempotency_key)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE event_log (
  id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,  -- monotonic cursor
  event_id    VARCHAR(64) NOT NULL,                         -- ULID, exposed to clients
  tenant_id   VARCHAR(64) NOT NULL,
  session_id  VARCHAR(64) NOT NULL,
  type        VARCHAR(64) NOT NULL,
  payload     JSON NOT NULL,
  created_at  BIGINT NOT NULL,
  KEY idx_event_cursor (tenant_id, session_id, id),
  UNIQUE KEY uq_event_id (event_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

**Retention:** daily prune of `event_log`/`messages`/`webhook_deliveries` older than `RETENTION_DAYS` (`0` = keep forever).

---

## 6. WhatsApp lifecycle, pairing & the admin number

**Pairing:** QR — `Connect()` on an empty device emits `QREvent`; stream the QR string (refreshes ~20s) for the UI to render. Pairing code — `PairPhone(ctx, phone, true, PairClientChrome, name)` returns a linking code for WhatsApp's "Link with phone number"; exposed via `POST /sessions/{id}/pairing-code`.

**Admin number bootstrap:** `WHATSAPP_ADMIN_NUMBER` declares the system admin number. On boot, if no valid keystore session exists for it, create an `is_admin_session=1` session and **print the pairing code to console + surface it in the admin panel**. The admin links it once. It does double duty: also addable as a normal number in the dashboard for regular API use. `WHATSAPP_ADMIN_CMD_PREFIX=am` — inbound messages on the admin session whose body starts with the prefix (e.g. `amlogin 123456`) are routed to the internal **command interceptor** (§7) and are **not** persisted, **not** emitted downstream, **not** counted as contacts. v1 ships the interceptor + a no-op registry; `amlogin` (WhatsApp-as-login) is v2.

---

## 7. Inbound pipeline

Ordered stages per whatsmeow event (tagged with session/tenant):

1. **Normalize** → versioned envelope (§9); never expose raw protobufs.
2. **Command interceptor** (admin session) → if inbound and body starts with `WHATSAPP_ADMIN_CMD_PREFIX`, hand to the command registry and **drop**.
3. **Identity/contacts capture** → upsert `whatsapp_identities` (push name, phone/LID resolution); upsert `whatsapp_contacts` for this account (set `seen_in_dm` + DM timestamps for DMs; bump `message_count`/`last_seen_at`). For groups: upsert `whatsapp_groups` + `whatsapp_group_members` (with `group_nickname`, role). Prefer push name for display.
4. **Persist** → upsert `chats`; insert `messages` (+`raw_json`); insert `poll_votes` on `DecryptPollVote`; update `messages.status`/`ack_level` on receipts.
5. **Auto-read** (if `auto_read`) → send read receipt before any reply, avoiding stuck-unread state on WhatsApp; optional `presence_typing` "composing" before outbound replies.
6. **Fan-out** → Redis pub/sub (stream subscribers) + enqueue webhook deliveries + append `event_log`.

Source-level `ignore` rules skip persistence/fan-out for status/groups/channels/broadcast as configured.

---

## 8. Outbound pipeline

**Unified send** with a typed body. Modes: **sync** (default; block on whatsmeow ack, return status + `wa_message_id`) and **async** (`?async=true`; persist to `outbox`, return `202` + `outbox_id`, final status via `message.status`; Redis-backed `asynq`). **Idempotency:** `Idempotency-Key` header, unique per tenant; replays return the original result. **Rate limiting:** per-session token bucket in Redis (`rate_per_min`/`rate_per_hour`); over-limit → `429` (sync) or deferred (async); optional jittered pacing.

Supported types: `text`, `poll`, `poll_vote`, `location`, `contact`, plus message ops (reaction/edit/revoke/forward) as sub-resources (§11). Media types parse but return `501` in v1.

---

## 9. Eventing & event schema

Same envelope on both transports.

**NDJSON HTTP stream (not SSE):** `GET /api/v1/events?session={id}&events=*` — `application/x-ndjson`, chunked, one JSON/line. `events=*` subscribes to all types (or comma-list). Auth by **header** (`Authorization: Bearer <key>` for programmatic, or cookie for the dashboard — same endpoint). Heartbeat `{"event":"ping",...}` every ~20s. Resume via `since={eventId}` (replays from `event_log`, then tails). `session` is an optional filter; omit to stream all your sessions.

**Webhooks (server-to-server):** per tenant/session or global. Headers: `X-Webhook-Request-Id`, `X-Webhook-Timestamp`; with HMAC `X-Webhook-Hmac` + `X-Webhook-Hmac-Algorithm: sha512`; plus `customHeaders`. Retries per `retry_policy`, tracked in `webhook_deliveries`, exhausted → `dead`; dedup by `event_id`. `events:["*"]` for all.

**Envelope (versioned):**
```json
{ "schema":"v1","id":"evt_01J9…","event":"message","session":"sess_01J8…",
  "tenant":"ten_abc","timestamp":1719400000000,"payload":{} }
```

**Catalog (v1):** `session.status` · `auth.qr` · `auth.code` · `message` · `message.from_me` · `message.status` · `message.reaction` · `message.edited` · `message.revoked` · `poll.vote` · `presence.update` · `group.update` · `group.participant` · `chat.update` · `contact.update` · `call.incoming` · `newsletter.update`. `media` is always metadata-only in v1 (`hasMedia:true, media:null`).

---

## 10. Auth, tenancy & API keys (Authula)

Authula embedded as a Go library, MySQL-backed, with **Secondary Storage** on Redis. Mounted under `/auth`. Plugins used:

| Plugin | Role here |
|---|---|
| Email & Password | login + registration (registration gated by `USER_PANEL_ENABLED`) |
| Session | dashboard cookie sessions |
| CSRF | CSRF protection for cookie routes |
| TOTP | optional 2FA |
| Access Control | RBAC — roles `super_admin` / `user`; gates admin routes |
| Admin | tenant (user) CRUD, ban/unban (= disable tenant), login-session revoke, **impersonation** (support/debug), audit trail — exposed under `/auth/admin/*` |
| Rate Limit | protect auth endpoints |

> **Naming:** Authula "sessions" = web login sessions (`/auth/*`). Our "WhatsApp sessions" = attached numbers (`/api/v1/sessions`). Distinct concepts.

**Roles:** `super_admin` — bootstrapped from `ADMIN_EMAIL`/`ADMIN_PASSWORD` (v1); manages all tenants/sessions via the Admin plugin. *(WhatsApp-as-login via `amlogin` is the planned v2 replacement; the admin number + interceptor ship now.)* `user` — self-registers, attaches own number(s), manages own keys/webhooks, consumes own events; data isolated by `tenant_id`. `USER_PANEL_ENABLED=false` → admin-only deployment.

**API keys (custom — Authula has no key plugin).** Account-global: a key belongs to a tenant and is valid across all that tenant's WhatsApp sessions; you target a session by **route** (`/sessions/{id}/…`), not by key. Permissions `{read,send,manage,events}`. `scope=global` (super_admin only) spans all tenants. Full key (`wak_<random>`) shown once; stored as argon2id hash + prefix; rotatable; `expires_at`; `last_used_at` tracked.

**Secrets at rest:** keys hashed; webhook HMAC secrets + sensitive config AES-GCM-encrypted with `APP_ENCRYPTION_KEY`. Protect the keystore DB (holds crypto material).

---

## 11. REST API (clean design)

**Principles:** resource-oriented and predictable; **one way to do a thing** (a single send endpoint, not per-type endpoints); consistent response/error/pagination envelopes; **session is always a path param, the API key is account-global**; plural nouns + standard verbs; non-CRUD actions as explicit sub-resources (`:start`, `/reaction`). Base `/api/v1`. Errors:
```json
{ "error": { "code": "rate_limited", "message": "…", "details": {} } }
```
Lists: `?limit=&cursor=` (opaque cursor over `id`). Spec of record: `docs/openapi.yaml` (**no Swagger UI**; raw spec optionally served at `/api/v1/openapi.yaml`).

### Sessions
| Method | Path |
|---|---|
| POST | `/sessions` — create `{label,start?,autoRead?,presenceTyping?}` |
| GET / GET | `/sessions` · `/sessions/{id}` |
| POST | `/sessions/{id}:start` · `:stop` · `:restart` · `:logout` |
| DELETE | `/sessions/{id}` |
| GET | `/sessions/{id}/me` |
| GET | `/sessions/{id}/qr` (`?format=image`) |
| POST | `/sessions/{id}/pairing-code` `{phone}` |

### Messages
| Method | Path | Notes |
|---|---|---|
| POST | `/sessions/{id}/messages` | send any type (typed body); `Idempotency-Key`, `?async` |
| PATCH | `/sessions/{id}/messages/{mid}` | edit text |
| DELETE | `/sessions/{id}/messages/{mid}` | revoke (delete for everyone) |
| POST | `/sessions/{id}/messages/{mid}/reaction` `{emoji}` · DELETE to remove |
| POST | `/sessions/{id}/messages/{mid}/forward` `{to}` |
| POST | `/sessions/{id}/messages/{mid}/vote` `{options:[]}` (poll vote) |

Send body (discriminated on `type`):
```json
{ "type":"text", "to":"628123@s.whatsapp.net", "text":"hi",
  "replyTo":"wa_msg_id", "mentions":["628999@s.whatsapp.net"] }
```
```json
{ "type":"poll", "to":"12036@g.us", "name":"Lunch?", "options":["Pizza","Sushi"], "selectableCount":1 }
```
```json
{ "type":"location", "to":"628123@s.whatsapp.net", "latitude":-8.65, "longitude":115.21, "name":"Denpasar" }
```
`type:"image"|"video"|"audio"|"document"|"sticker"` → `501` in v1.

### Chats (viewer + read API)
| GET | `/sessions/{id}/chats` · `/sessions/{id}/chats/{cid}` · `/sessions/{id}/chats/{cid}/messages` |
| POST | `/sessions/{id}/chats/{cid}/read` `{messageIds?}` |
| PATCH | `/sessions/{id}/chats/{cid}` (archive/pin/mute) |
| DELETE | `/sessions/{id}/chats/{cid}` |
| PUT | `/sessions/{id}/chats/{cid}/presence` `{state:"composing|paused|recording"}` |

### Contacts (the "found users" feature)
| Method | Path | Notes |
|---|---|---|
| GET | `/sessions/{id}/contacts` | found users + where-found; filters `?source=dm\|group`, `?group={jid}`, `?q=` |
| GET | `/sessions/{id}/contacts/{lid}` | identity + `{dm, groups:[{jid,name,nickname,role,lastSeen}]}` |
| GET | `/sessions/{id}/contacts/check?phone=` | on-WhatsApp check |
| GET | `/sessions/{id}/contacts/{jid}/picture` · `/about` |
| POST | `/sessions/{id}/contacts/{jid}/block` · `/unblock` |

Contact list/detail prefer push name; `nickname` is per-group (from the group membership pivot). `GET /contacts/{lid}` (no session prefix, super_admin) resolves cross-account sightings.

### Groups
| POST/GET/GET | `/sessions/{id}/groups` (create) · list · `/groups/{gid}` |
| GET | `/sessions/{id}/groups/{gid}/members` (incl. `group_nickname`, role) |
| POST | `/sessions/{id}/groups/{gid}/members` `{participants}` (add) |
| DELETE | `/sessions/{id}/groups/{gid}/members/{jid}` (remove) |
| POST | `/sessions/{id}/groups/{gid}/members/{jid}/promote` · `/demote` |
| PATCH | `/sessions/{id}/groups/{gid}` (subject/description/announce/locked) |
| GET/DELETE | `/sessions/{id}/groups/{gid}/invite` (get link / revoke) |
| POST | `/sessions/{id}/groups:join` `{invite}` · `/sessions/{id}/groups/{gid}:leave` |
| POST | `/sessions/{id}/groups/{gid}/members:approve` (pending) |

### Channels · Status · Presence
| POST | `/sessions/{id}/channels` (create) · `/channels/{jid}:follow` · `:unfollow` · `:mute` · `/channels/{jid}/messages` |
| POST | `/sessions/{id}/status` (text now; image `501`) |
| PUT | `/sessions/{id}/presence` `{state:"online|offline"}` |

### Events · Keys · Webhooks · Admin · Health
| GET | `/events` (NDJSON, §9) |
| POST/GET/PATCH/DELETE | `/webhooks` · `/webhooks/{id}` |
| POST/GET/DELETE | `/keys` · `/keys/{id}` (create returns full key once); POST `/keys/{id}:rotate` |
| (Authula) | `/auth/admin/*` — tenant/user mgmt, ban, impersonation |
| GET | `/api/v1/admin/sessions` — cross-tenant WhatsApp oversight (super_admin) |
| GET | `/healthz` · `/readyz` · `/metrics` (Prometheus) |

---

## 12. Configuration (ENV)

| Var | Default | Purpose |
|---|---|---|
| `HTTP_ADDR` | `:8080` | listen addr |
| `PUBLIC_URL` | — | external base URL |
| `APP_ENCRYPTION_KEY` | — | base64 32-byte AES-GCM key |
| `MYSQL_DSN` | — | app data DSN |
| `WHATSMEOW_STORE_DRIVER` | `sqlite` | `mysql` \| `sqlite` |
| `WHATSMEOW_STORE_DSN` | `file:store.db?_foreign_keys=on` | keystore DSN |
| `REDIS_URL` | — | queue/cache/pubsub/limits (required for async + stream fan-out) |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` | — | bootstrap super-admin |
| `WHATSAPP_ADMIN_NUMBER` | — | admin number to provision/pair on boot |
| `WHATSAPP_ADMIN_CMD_PREFIX` | `am` | private command prefix on admin session |
| `USER_PANEL_ENABLED` | `true` | self-registration + user UI |
| `DEFAULT_RATE_PER_MIN` / `DEFAULT_RATE_PER_HOUR` | `20` / `200` | per-session send limits |
| `DEFAULT_AUTO_READ` | `true` | mark inbound read before replying |
| `IGNORE_STATUS` / `IGNORE_GROUPS` / `IGNORE_CHANNELS` / `IGNORE_BROADCAST` | `false` | source-level filtering |
| `WEBHOOK_URL` / `WEBHOOK_EVENTS` / `WEBHOOK_HMAC_KEY` / `WEBHOOK_RETRIES_*` | — | global webhook defaults |
| `RETENTION_DAYS` | `0` | prune old data (0 = keep) |
| `LOG_LEVEL` | `info` | structured logging |

---

## 13. Frontend (realtime dashboard)

Init: `pnpm dlx shadcn@latest init --preset b7BFcVOXQ --base base --template react-router`.

**Serving:** build in **SPA mode** to static assets, embed in the Go binary (`embed.FS`); Go serves SPA + API on one port → single-container. (Data is fetched client-side from the JSON API; if you later want SSR, run the React Router Node server as a sidecar — API unchanged.)

**Realtime-ish:** the dashboard opens the **NDJSON event stream** (`GET /api/v1/events?events=*`) using the **cookie session** (browser `fetch` + `ReadableStream`), and feeds events into the data layer (TanStack Query cache updates / live state). Live surfaces: session statuses, the admin-number/QR/pairing code, the viewer's incoming messages, and an event monitor. Auto-reconnect with `since={lastEventId}`; short-interval polling fallback if the stream drops.

**Surfaces:** **Admin** — tenants (via Authula Admin plugin: list/ban/impersonate), all WhatsApp sessions + statuses, admin-number pairing code, event monitor. **User** (toggleable) — my sessions (create/start/stop/QR/pairing), my keys, my webhooks, viewer. **Viewer** (read-only) — chats + message timeline; media shows a "not downloaded" placeholder. **Contacts** — searchable found-users list; drill into a contact to see DM + groups (with group names + per-group nicknames).

---

## 14. Packaging

**Dockerfile (multi-stage):**
```dockerfile
FROM node:22-alpine AS web
WORKDIR /web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build                       # SPA -> /web/build/client

FROM golang:1.26-alpine AS api       # match go.mod's "go 1.26"
WORKDIR /src
RUN apk add --no-cache gcc musl-dev  # CGO for sqlite fallback
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/build/client ./internal/http/static/dist
RUN CGO_ENABLED=1 go build -o /out/gateway ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=api /out/gateway /usr/local/bin/gateway
EXPOSE 8080
ENTRYPOINT ["gateway"]
```
*(MySQL-only keystore → set `CGO_ENABLED=0`, drop the build deps, smaller static image.)*

**Go toolchain:** `go.mod` declares `go 1.26`. Keep the builder image matched to it — with a lower base image the go command would auto-download a 1.26 toolchain at build time (needs network). One recent host Go (≥1.21) is enough to bootstrap: `GOTOOLCHAIN=auto` (the default) fetches and switches to the version `go.mod` requires on demand and never downgrades; use `GOTOOLCHAIN=local` in CI for reproducible, offline builds. The optional `toolchain go1.26.x` line in `go.mod` (added by `go get go@1.26`) pins the exact version.

**`docker-compose.yml` — local, DB included:**
```yaml
services:
  gateway:
    build: .
    ports: ["8080:8080"]
    environment:
      MYSQL_DSN: "gw:gwpass@tcp(mysql:3306)/gateway?parseTime=true&charset=utf8mb4"
      WHATSMEOW_STORE_DRIVER: "mysql"
      WHATSMEOW_STORE_DSN: "gw:gwpass@tcp(mysql:3306)/gateway?parseTime=true&charset=utf8mb4"
      REDIS_URL: "redis://redis:6379"
      APP_ENCRYPTION_KEY: "${APP_ENCRYPTION_KEY}"
      ADMIN_EMAIL: "${ADMIN_EMAIL}"
      ADMIN_PASSWORD: "${ADMIN_PASSWORD}"
      WHATSAPP_ADMIN_NUMBER: "${WHATSAPP_ADMIN_NUMBER}"
    depends_on: [mysql, redis]
  mysql:
    image: mysql:8.4
    environment: { MYSQL_DATABASE: gateway, MYSQL_USER: gw, MYSQL_PASSWORD: gwpass, MYSQL_ROOT_PASSWORD: rootpass }
    command: ["--character-set-server=utf8mb4","--collation-server=utf8mb4_unicode_ci"]
    volumes: ["mysql_data:/var/lib/mysql"]
  redis:
    image: redis:7-alpine
    volumes: ["redis_data:/data"]
volumes: { mysql_data: {}, redis_data: {} }
```

**`docker-compose.external.yml` — prod, bring-your-own DB (only the app):**
```yaml
services:
  gateway:
    image: ghcr.io/you/whatsmeow-gateway:latest
    ports: ["8080:8080"]
    environment:
      MYSQL_DSN: "${MYSQL_DSN}"
      WHATSMEOW_STORE_DRIVER: "mysql"
      WHATSMEOW_STORE_DSN: "${MYSQL_DSN}"
      REDIS_URL: "${REDIS_URL}"
      APP_ENCRYPTION_KEY: "${APP_ENCRYPTION_KEY}"
      ADMIN_EMAIL: "${ADMIN_EMAIL}"
      ADMIN_PASSWORD: "${ADMIN_PASSWORD}"
      WHATSAPP_ADMIN_NUMBER: "${WHATSAPP_ADMIN_NUMBER}"
      USER_PANEL_ENABLED: "${USER_PANEL_ENABLED:-true}"
```
MySQL keystore means prod needs only your shared MySQL + Redis.

---

## 15. Repo layout (monorepo)

```
.
├── cmd/server/main.go
├── internal/
│   ├── config/                 # ENV load + validate
│   ├── http/
│   │   ├── router.go           # chi routes
│   │   ├── middleware/         # apikey, cookie-session(authula), ratelimit, recover, log
│   │   ├── handlers/           # sessions, messages, chats, contacts, groups, channels,
│   │   │                       #   events, webhooks, keys, admin, health
│   │   └── static/             # embedded SPA dist
│   ├── auth/                   # Authula wiring (plugins), RBAC glue, bootstrap admin
│   ├── wa/
│   │   ├── manager.go          # session map, connect/reconnect
│   │   ├── session.go          # per-client wrapper + status
│   │   ├── store/mysql/        # whatsmeow store interfaces over MySQL
│   │   ├── inbound/            # normalize, capture, command interceptor, auto-read
│   │   ├── outbound/           # send, idempotency, ratelimit, outbox worker
│   │   └── events/             # envelope, catalog, ignore rules
│   ├── store/                  # MySQL repos (tenants, sessions, messages, contacts…)
│   ├── webhooks/               # dispatcher, retries, hmac, dedup
│   ├── stream/                 # NDJSON endpoint + redis pubsub fan-out
│   └── queue/                  # asynq jobs
├── migrations/                 # golang-migrate (app schema)
├── web/                        # React Router + shadcn SPA
├── deploy/                     # docker-compose.yml · .external.yml · .dev.yml · Dockerfile · .env.example
├── docs/
│   ├── openapi.yaml            # API spec of record
│   └── specs/                  # *.md design specs — one per subsystem/feature (REQUIRED, see §19)
├── .air.toml                   # backend hot-reload (dev)
├── Makefile                    # dev / build / test / lint targets
└── README.md
```

---

## 16. Milestones

- **M0 — Scaffolding:** monorepo, chi server, config, migrations, both compose files, CI, embedded SPA shell, `docs/specs/` seeded with a stub per subsystem (filled in as each milestone lands).
- **M1 — Auth & tenancy:** Authula plugins (Email&Password, Session, CSRF, TOTP, Access Control, Admin, Rate Limit, Secondary Storage), cookie sessions, RBAC, admin bootstrap, `USER_PANEL_ENABLED`.
- **M2 — Sessions:** Session Manager, **MySQL keystore** (SQLite first → MySQL), QR + pairing-code, admin-number bootstrap + code surfacing, status events, reconnect/logout.
- **M3 — Inbound:** normalization + envelope, identity/contacts/groups capture, message/chat/poll-vote persistence, command interceptor, auto-read + typing.
- **M4 — Outbound:** unified send + message ops, idempotency, sync + async outbox, rate limiting.
- **M5 — Eventing:** NDJSON stream (`events=*`, heartbeat, cursor resume) + webhooks (HMAC, retries, dedup, dead-letter) + catalog.
- **M6 — API & keys:** account-global keys (permissions/rotation), chats/contacts/groups/channels/status/presence endpoints, `openapi.yaml`.
- **M7 — Frontend:** admin + user panels, viewer, Contacts, realtime stream wiring, QR/pairing UX, key/webhook management.
- **M8 — Hardening:** at-rest encryption, retention/prune, metrics/health, structured logs, ToS disclaimer, e2e smoke against a test number.

---

## 17. Deferred (v2+)

Media subsystem (download/upload, local/S3 storage, thumbnails, size limits) — schema reserves `has_media`/`media_meta`, media sends `501`. Horizontal scaling via session-sharding. Proxy per session. WhatsApp-as-login (`amlogin`). Business labels. Authula **Organizations** plugin if tenants ever need multiple member-users; OAuth2 / Magic Link / JWT+Bearer if alternative auth is wanted later.

---

## 18. Open micro-decisions (sensible defaults assumed)

1. **History sync on pair** — ingest past messages so the viewer isn't empty? Default **off** (`INGEST_HISTORY` toggle).
2. **Own-number outbound echo** — store/emit phone-originated sends as `message.from_me`? Default **yes**.
3. **Rate-limit in sync mode** — `429` vs auto-queue. Default **429**; async queues.
4. **Migrations** — `golang-migrate` vs `goose`. Default **golang-migrate**.

Next deliverable: full `docs/openapi.yaml` + the whatsmeow MySQL store skeleton (interface stubs + SQLite→MySQL DDL translation) to start M2.

---

## 19. Engineering conventions

Clean, maintainable, concise:

- **Go:** idiomatic, small focused packages; interfaces only at real boundaries (store, wa client, event sink) and defined by the consumer; constructor injection, no global singletons; `context.Context` first arg everywhere; errors wrapped with `%w`; `log/slog` structured logging; table-driven tests; `golangci-lint` in CI; no premature abstraction or generics-for-their-own-sake.
- **HTTP:** one handler per endpoint, thin — validate → call service → encode; shared request/response/error helpers; services hold business logic; repos hold SQL. No ORM (plain `database/sql` + a thin query helper).
- **Frontend:** typed API client generated from `openapi.yaml`; TanStack Query for fetching + the NDJSON stream for realtime; shadcn primitives; minimal global state; colocate components with routes.
- **General:** prefer composition over configuration flags; keep functions short; comment the *why*, not the *what*.
- **Documentation (`docs/specs/*.md`):** every subsystem/feature carries a markdown design spec under `docs/specs/` — one file per area (e.g. `session-manager.md`, `inbound-pipeline.md`, `outbound-pipeline.md`, `eventing.md`, `auth-tenancy.md`, `api-keys.md`, `webhooks.md`, `contacts.md`, `whatsmeow-store.md`, `frontend.md`). Treated as required documentation, not optional: a spec is written/updated **in the same change** as the code it describes, kept in sync (it's the source of truth for behavior and decisions), and reviewed alongside it. This masterplan is the top-level overview; `docs/specs/*.md` are the detailed, living specs; `docs/openapi.yaml` remains the API contract of record.
- **Commits:** Conventional-Commits-style prefixes — `feature:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`. Commit **often** with `feature:*` (and the others) to checkpoint progress in small, working increments rather than large drops — each commit should build and leave the tree in a coherent state, and touch its `docs/specs/*.md` when behavior changes.

---

## 20. Local development

Fast inner loop: **infra in Docker, app on the host.** MySQL + Redis run in a tiny infra-only compose (ports bound to localhost); the Go backend runs under `air` (hot reload) and the frontend under Vite (HMR). With the MySQL keystore you build `CGO_ENABLED=0`, so no C compiler is needed. *(Zero-install alternative: a `.devcontainer` running the Go + Node toolchain — same loop, inside a container. Authula ships one for reference.)*

**Host prerequisites (native dev):** Go 1.26+ (one recent Go ≥1.21 bootstraps it; the toolchain auto-switches per `go.mod` — see §14), Node 22+ with pnpm (`corepack enable`), `air` (`go install github.com/air-verse/air@latest`), `golangci-lint`, Docker (for infra). Docker alone is enough to *run* the stack, but not for an efficient edit loop — you want a host toolchain for `gopls`, `go test`, the linter, and `delve` debugging.

**`deploy/docker-compose.dev.yml` — infra only (no app):**
```yaml
services:
  mysql:
    image: mysql:8.4
    ports: ["3306:3306"]
    environment: { MYSQL_DATABASE: gateway, MYSQL_USER: gw, MYSQL_PASSWORD: gwpass, MYSQL_ROOT_PASSWORD: rootpass }
    command: ["--character-set-server=utf8mb4","--collation-server=utf8mb4_unicode_ci"]
    volumes: ["mysql_dev:/var/lib/mysql"]
    healthcheck:
      test: ["CMD","mysqladmin","ping","-h","localhost","-prootpass"]
      interval: 5s
      timeout: 3s
      retries: 10
  redis:
    image: redis:7-alpine
    ports: ["6379:6379"]
    volumes: ["redis_dev:/data"]
volumes: { mysql_dev: {}, redis_dev: {} }
```

**`.env` (dev — host app → Dockerized infra; gitignored):**
```sh
HTTP_ADDR=:8080
MYSQL_DSN=gw:gwpass@tcp(127.0.0.1:3306)/gateway?parseTime=true&charset=utf8mb4
WHATSMEOW_STORE_DRIVER=mysql
WHATSMEOW_STORE_DSN=gw:gwpass@tcp(127.0.0.1:3306)/gateway?parseTime=true&charset=utf8mb4
REDIS_URL=redis://127.0.0.1:6379
APP_ENCRYPTION_KEY=        # openssl rand -base64 32
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=changeme
WHATSAPP_ADMIN_NUMBER=628xxxxxxxx
LOG_LEVEL=debug
```
The server loads `.env` on boot in dev (e.g. `godotenv`); it's a no-op if absent (prod injects real env).

**`.air.toml` — backend hot reload:**
```toml
root = "."
tmp_dir = "tmp"

[build]
  cmd = "CGO_ENABLED=0 go build -o ./tmp/gateway ./cmd/server"
  bin = "tmp/gateway"
  include_ext = ["go"]
  exclude_dir = ["tmp", "web", "deploy", "docs", ".git"]
  exclude_regex = ["_test.go"]
  delay = 300
  stop_on_error = true

[misc]
  clean_on_exit = true
```

**`web/vite.config.ts` — dev proxy to the Go server:**
```ts
server: {
  proxy: {
    // /api includes the NDJSON stream — proxied streaming works out of the box
    "/api":  { target: "http://localhost:8080", changeOrigin: true },
    "/auth": { target: "http://localhost:8080", changeOrigin: true },
  },
}
```

**`Makefile`:**
```make
COMPOSE_DEV = docker compose -f deploy/docker-compose.dev.yml

.PHONY: infra-up infra-down infra-reset dev web migrate build lint test tidy gen

infra-up:    ## start mysql + redis
	$(COMPOSE_DEV) up -d
infra-down:  ## stop infra (keep data)
	$(COMPOSE_DEV) down
infra-reset: ## stop infra + wipe data
	$(COMPOSE_DEV) down -v

dev:         ## backend hot-reload (run infra-up first)
	air
web:         ## frontend dev server (HMR)
	cd web && pnpm dev

migrate:     ## apply DB migrations
	migrate -path migrations -database "$$MYSQL_DSN" up   # or: go run ./cmd/server migrate up

build:       ## production image
	docker build -t whatsmeow-gateway -f deploy/Dockerfile .
lint:
	golangci-lint run
test:
	go test ./...
tidy:
	go mod tidy && cd web && pnpm install
gen:         ## regen typed API client from openapi.yaml
	cd web && pnpm openapi
```

**Day-to-day:** `make infra-up` once, then two terminals — `make dev` (backend) and `make web` (frontend). Open the Vite URL; `/api`, `/auth`, and the event stream proxy to the Go server. `make infra-reset` for a clean DB.