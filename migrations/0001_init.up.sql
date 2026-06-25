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
