-- v2 WhatsApp app-data plane (masterplan §7).
--
-- Conventions: utf8mb4 / utf8mb4_unicode_ci; timestamps are epoch-ms BIGINT;
-- surrogate BIGINT UNSIGNED AUTO_INCREMENT PKs where shown, VARCHAR(64) ULID PKs
-- where shown. Ownership is by `organization_id` (a better-auth organization id);
-- `created_by_user_id` is kept for audit. No v1 `tenants`/`api_keys` mirror.
--
-- better-auth's own tables (user/session/account/verification/jwks/apikey/
-- organization/member/invitation/...) are owned by the frontend (drizzle) and are
-- NOT defined here. The whatsmeow keystore (wmstore_*) lives in gateway-local
-- SQLite and is auto-migrated by whatsmeow's sqlstore — it is NOT in MySQL in v2.

-- Registry of gateways. One self-row in v2; rows added when sharding (forward-compat).
CREATE TABLE gateways (
  id                    VARCHAR(64) PRIMARY KEY,
  label                 VARCHAR(255) NULL,
  notes                 TEXT NULL,
  status                ENUM('pending_enrollment','joining','active','draining','drained','disabled') NOT NULL DEFAULT 'pending_enrollment',
  creator_kind          ENUM('system','user') NOT NULL DEFAULT 'system',
  created_by_user_id    VARCHAR(64) NULL,
  base_url              TEXT NULL, -- transitional HTTP proxy address; removable after gRPC cutover
  grpc_endpoint         VARCHAR(512) NULL,
  session_count         INT UNSIGNED NOT NULL DEFAULT 0,
  capacity              INT UNSIGNED NULL,
  desired_revision      BIGINT UNSIGNED NOT NULL DEFAULT 0,
  applied_revision      BIGINT UNSIGNED NOT NULL DEFAULT 0,
  software_version      VARCHAR(128) NULL,
  capabilities          JSON NULL,
  enrolled_at           BIGINT NULL,
  connected_at          BIGINT NULL,
  last_seen_at          BIGINT NULL,
  deleted_at            BIGINT NULL,
  created_at            BIGINT NOT NULL,
  updated_at            BIGINT NOT NULL,
  CONSTRAINT chk_gateway_creator CHECK (
    (creator_kind = 'system' AND created_by_user_id IS NULL) OR
    (creator_kind = 'user' AND created_by_user_id IS NOT NULL)
  ),
  CONSTRAINT chk_gateway_revisions CHECK (applied_revision <= desired_revision),
  KEY idx_gateways_status_seen (status, last_seen_at),
  KEY idx_gateways_creator (created_by_user_id),
  KEY idx_gateways_revision (desired_revision, applied_revision)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Opaque enrollment tokens. Only a SHA-256 digest and non-secret display prefix
-- persist; plaintext bearer tokens exist only in the eventual issue response.
CREATE TABLE gateway_enrollment_tokens (
  id                    VARCHAR(64) PRIMARY KEY,
  gateway_id            VARCHAR(64) NOT NULL,
  token_hash            BINARY(32) NOT NULL,
  token_prefix          VARCHAR(16) NOT NULL,
  status                ENUM('active','redeeming','consumed','revoked','locked') NOT NULL DEFAULT 'active',
  attempt_count         INT UNSIGNED NOT NULL DEFAULT 0,
  max_attempts          INT UNSIGNED NOT NULL DEFAULT 5,
  expires_at            BIGINT NOT NULL,
  redemption_nonce      BINARY(16) NULL,
  csr_sha256             BINARY(32) NULL,
  redeeming_at           BIGINT NULL,
  lease_expires_at       BIGINT NULL,
  consumed_at           BIGINT NULL,
  revoked_at            BIGINT NULL,
  created_by_user_id    VARCHAR(64) NOT NULL,
  created_at            BIGINT NOT NULL,
  updated_at            BIGINT NOT NULL,
  CONSTRAINT fk_enrollment_gateway FOREIGN KEY (gateway_id) REFERENCES gateways(id) ON DELETE RESTRICT,
  CONSTRAINT chk_enrollment_attempts CHECK (max_attempts > 0 AND attempt_count <= max_attempts),
  CONSTRAINT chk_enrollment_validity CHECK (expires_at > created_at),
  CONSTRAINT chk_enrollment_redeeming CHECK (
    (status = 'redeeming' AND redemption_nonce IS NOT NULL AND csr_sha256 IS NOT NULL AND redeeming_at IS NOT NULL AND lease_expires_at > redeeming_at) OR
    (status <> 'redeeming' AND redemption_nonce IS NULL AND csr_sha256 IS NULL AND redeeming_at IS NULL AND lease_expires_at IS NULL)
  ),
  CONSTRAINT chk_enrollment_terminal_time CHECK (
    (status = 'consumed' AND consumed_at IS NOT NULL AND revoked_at IS NULL) OR
    (status = 'revoked' AND revoked_at IS NOT NULL AND consumed_at IS NULL) OR
    (status NOT IN ('consumed','revoked') AND consumed_at IS NULL AND revoked_at IS NULL)
  ),
  UNIQUE KEY uq_enrollment_token_hash (token_hash),
  KEY idx_enrollment_gateway_status (gateway_id, status),
  KEY idx_enrollment_expiry (status, expires_at)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE pki_authorities (
  id                     VARCHAR(64) PRIMARY KEY,
  kind                   ENUM('root','intermediate') NOT NULL,
  parent_authority_id    VARCHAR(64) NULL,
  parent_kind            ENUM('root','intermediate') NULL,
  status                 ENUM('active','retiring','retired','revoked') NOT NULL,
  certificate_pem        MEDIUMTEXT NOT NULL,
  certificate_fingerprint BINARY(32) NOT NULL,
  encrypted_private_key  LONGBLOB NOT NULL,
  private_key_nonce      VARBINARY(32) NOT NULL,
  encryption_key_id      VARCHAR(255) NOT NULL,
  not_before             BIGINT NOT NULL,
  not_after              BIGINT NOT NULL,
  created_at             BIGINT NOT NULL,
  updated_at             BIGINT NOT NULL,
  active_kind            VARCHAR(16) GENERATED ALWAYS AS (CASE WHEN status = 'active' THEN kind ELSE NULL END) STORED,
  UNIQUE KEY uq_pki_id_kind (id, kind),
  CONSTRAINT fk_pki_parent FOREIGN KEY (parent_authority_id, parent_kind) REFERENCES pki_authorities(id, kind) ON DELETE RESTRICT,
  CONSTRAINT chk_pki_parent CHECK (
    (kind = 'root' AND parent_authority_id IS NULL AND parent_kind IS NULL) OR
    (kind = 'intermediate' AND parent_authority_id IS NOT NULL AND parent_kind = 'root' AND parent_authority_id <> id)
  ),
  UNIQUE KEY uq_pki_fingerprint (certificate_fingerprint),
  UNIQUE KEY uq_pki_single_active_kind (active_kind),
  CONSTRAINT chk_pki_validity CHECK (not_after > not_before),
  KEY idx_pki_status_expiry (status, not_after)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Stable singleton used solely to serialize authority rotation transactions.
CREATE TABLE pki_rotation_lock (
  id TINYINT PRIMARY KEY,
  CONSTRAINT chk_pki_rotation_lock CHECK (id=1)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
INSERT INTO pki_rotation_lock (id) VALUES (1);

CREATE TABLE gateway_certificates (
  id                      VARCHAR(64) PRIMARY KEY,
  gateway_id              VARCHAR(64) NOT NULL,
  authority_id            VARCHAR(64) NOT NULL,
  enrollment_token_id     VARCHAR(64) NOT NULL,
  csr_sha256               BINARY(32) NOT NULL,
  serial_number           VARCHAR(128) NOT NULL,
  certificate_pem         MEDIUMTEXT NOT NULL,
  trust_bundle_pem        MEDIUMTEXT NOT NULL,
  certificate_fingerprint BINARY(32) NOT NULL,
  not_before              BIGINT NOT NULL,
  not_after               BIGINT NOT NULL,
  revoked_at              BIGINT NULL,
  revocation_reason       VARCHAR(255) NULL,
  created_at              BIGINT NOT NULL,
  CONSTRAINT fk_gateway_certificate_gateway FOREIGN KEY (gateway_id) REFERENCES gateways(id) ON DELETE RESTRICT,
  CONSTRAINT fk_gateway_certificate_authority FOREIGN KEY (authority_id) REFERENCES pki_authorities(id),
  CONSTRAINT fk_gateway_certificate_token FOREIGN KEY (enrollment_token_id) REFERENCES gateway_enrollment_tokens(id) ON DELETE RESTRICT,
  CONSTRAINT chk_gateway_certificate_validity CHECK (not_after > not_before),
  CONSTRAINT chk_gateway_certificate_revocation CHECK (
    (revoked_at IS NULL AND revocation_reason IS NULL) OR
    (revoked_at IS NOT NULL AND revocation_reason IS NOT NULL)
  ),
  UNIQUE KEY uq_gateway_certificate_serial (authority_id, serial_number),
  UNIQUE KEY uq_gateway_certificate_fingerprint (certificate_fingerprint),
  UNIQUE KEY uq_gateway_certificate_token_csr (enrollment_token_id, csr_sha256),
  KEY idx_gateway_certificate_gateway_expiry (gateway_id, not_after),
  KEY idx_gateway_certificate_revoked (gateway_id, revoked_at)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE audit_events (
  id               VARCHAR(64) PRIMARY KEY,
  organization_id  VARCHAR(64) NULL,
  actor_type       ENUM('user','gateway','system') NOT NULL,
  actor_id         VARCHAR(64) NULL,
  action           VARCHAR(128) NOT NULL,
  resource_type    VARCHAR(64) NOT NULL,
  resource_id      VARCHAR(64) NULL,
  outcome          ENUM('success','denied','failure') NOT NULL,
  request_id       VARCHAR(128) NULL,
  source_ip        VARBINARY(16) NULL,
  metadata         JSON NULL,
  created_at       BIGINT NOT NULL,
  KEY idx_audit_resource (resource_type, resource_id, created_at),
  KEY idx_audit_actor (actor_type, actor_id, created_at),
  KEY idx_audit_org_time (organization_id, created_at)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wa_sessions (
  id                 VARCHAR(64) PRIMARY KEY,       -- app session id (ULID)
  organization_id    VARCHAR(64) NOT NULL,          -- = better-auth organization id (owner)
  created_by_user_id VARCHAR(64) NULL,              -- better-auth user id (audit: who created it)
  gateway_id         VARCHAR(64) NOT NULL,          -- which gateway holds this session's keystore
  label             VARCHAR(255) NULL,
  status            ENUM('starting','scan_qr_code','working','failed','stopped','logged_out') NOT NULL DEFAULT 'stopped',
  wa_jid            VARCHAR(255) NULL,
  wa_lid            VARCHAR(255) NULL,
  phone_number      VARCHAR(64) NULL,
  is_admin_session  TINYINT(1) NOT NULL DEFAULT 0,  -- the WHATSAPP_ADMIN_NUMBER session
  auto_read         TINYINT(1) NOT NULL DEFAULT 1,
  presence_typing   TINYINT(1) NOT NULL DEFAULT 0,
  rate_per_min      INT NOT NULL DEFAULT 20,
  rate_per_hour     INT NOT NULL DEFAULT 200,
  last_connected_at BIGINT NULL,
  created_at        BIGINT NOT NULL,
  updated_at        BIGINT NOT NULL,
  KEY idx_sessions_org (organization_id),
  KEY idx_sessions_gateway (gateway_id),
  UNIQUE KEY uq_sessions_jid (wa_jid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE webhooks (
  id              VARCHAR(64) PRIMARY KEY,
  organization_id VARCHAR(64) NOT NULL,
  session_id      VARCHAR(64) NULL,                 -- null = all the org's sessions
  url            TEXT NOT NULL,
  events         JSON NOT NULL,                     -- ["message","poll.vote"] or ["*"]
  hmac_secret    VARBINARY(512) NULL,               -- AES-GCM encrypted at rest
  custom_headers JSON NULL,
  retry_policy   JSON NOT NULL,                     -- {"policy":"exponential","delaySeconds":2,"attempts":15}
  active         TINYINT(1) NOT NULL DEFAULT 1,
  created_at     BIGINT NOT NULL,
  updated_at     BIGINT NOT NULL,
  KEY idx_webhooks_org (organization_id)
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

-- ===== Identity / Contacts model (global; not user-scoped) =====

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

CREATE TABLE whatsapp_contacts (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id    VARCHAR(64) NOT NULL,               -- the account that encountered them
  lid           VARCHAR(255) NOT NULL,              -- identifier as encountered: "<n>@lid" or "<phone>@s.whatsapp.net"
  phone         VARCHAR(64)  NULL,                  -- bare phone, set only when `lid` is a "@s.whatsapp.net" JID
  seen_in_dm    TINYINT(1) NOT NULL DEFAULT 0,
  dm_first_seen_at BIGINT NULL,
  dm_last_seen_at  BIGINT NULL,
  message_count BIGINT NOT NULL DEFAULT 0,
  first_seen_at BIGINT NOT NULL,
  last_seen_at  BIGINT NOT NULL,
  UNIQUE KEY uq_contact (session_id, lid),
  KEY idx_contact_lid (lid),
  KEY idx_contact_phone (phone)
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

CREATE TABLE whatsapp_group_members (
  id             BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id     VARCHAR(64) NOT NULL,
  group_jid      VARCHAR(255) NOT NULL,
  lid            VARCHAR(255) NOT NULL,
  group_nickname TEXT NULL,
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
  id                VARCHAR(64) PRIMARY KEY,            -- msg_<ULID>, monotonic sortable cursor
  session_id        VARCHAR(64) NOT NULL,
  wa_message_id     VARCHAR(255) NOT NULL,
  chat_jid          VARCHAR(255) NOT NULL,
  sender_lid        VARCHAR(255) NULL,
  sender_jid        VARCHAR(255) NULL,
  from_me           TINYINT(1) NOT NULL DEFAULT 0,
  direction         ENUM('in','out') NOT NULL,
  type              VARCHAR(32) NOT NULL,
  body              MEDIUMTEXT NULL,
  quoted_message_id VARCHAR(255) NULL,
  mentions          JSON NULL,
  has_media         TINYINT(1) NOT NULL DEFAULT 0,
  media_meta        JSON NULL,                       -- NOT downloaded in v2
  status            ENUM('pending','sent','delivered','read','played','failed') NULL,
  ack_level         INT NULL,
  error             TEXT NULL,
  edited            TINYINT(1) NOT NULL DEFAULT 0,
  deleted           TINYINT(1) NOT NULL DEFAULT 0,
  timestamp         BIGINT NOT NULL,
  raw_json          JSON NULL,
  created_at        BIGINT NOT NULL,
  UNIQUE KEY uq_msg (session_id, wa_message_id),
  KEY idx_msg_chat (session_id, chat_jid, id),
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
  UNIQUE KEY uq_pollvote_event (session_id, poll_message_id, voter_lid, timestamp),
  KEY idx_pollvote (session_id, poll_message_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE outbox (
  id              VARCHAR(64) PRIMARY KEY,            -- ULID
  organization_id VARCHAR(64) NOT NULL,
  session_id      VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(255) NULL,
  payload         JSON NOT NULL,
  status          ENUM('queued','sending','sent','failed') NOT NULL DEFAULT 'queued',
  attempts        INT NOT NULL DEFAULT 0,
  wa_message_id   VARCHAR(255) NULL,
  error           TEXT NULL,
  created_at      BIGINT NOT NULL,
  updated_at      BIGINT NOT NULL,
  UNIQUE KEY uq_idem (organization_id, idempotency_key)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE event_log (
  id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,  -- monotonic cursor
  event_id    VARCHAR(64) NOT NULL,                         -- ULID, exposed to clients
  organization_id VARCHAR(64) NOT NULL,
  session_id  VARCHAR(64) NOT NULL,
  type        VARCHAR(64) NOT NULL,
  payload     JSON NOT NULL,
  created_at  BIGINT NOT NULL,
  KEY idx_event_cursor (organization_id, session_id, id),
  UNIQUE KEY uq_event_id (event_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
