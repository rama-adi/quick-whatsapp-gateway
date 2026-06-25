-- whatsmeow device keystore tables (MySQL backend).
--
-- Mechanically translated from go.mau.fi/whatsmeow/store/sqlstore/upgrades
-- (Postgres/SQLite v14 schema) to MySQL:
--   * TEXT keys that participate in PRIMARY/FOREIGN keys become VARCHAR(128)
--     (utf8mb4 needs a bounded length for indexed columns; 128 keeps even the
--     four-column PK of wmstore_message_secrets — 4*128*4 = 2048 bytes — under
--     InnoDB's 3072-byte index-prefix limit, while WhatsApp JIDs/message-ids
--     are far shorter than 128 chars). Non-indexed free-text columns (device
--     platform/business_name/push_name, retry format) stay VARCHAR(255).
--   * bytea -> VARBINARY (fixed/short) or BLOB/LONGBLOB (variable/large).
--   * BOOLEAN -> TINYINT(1).
--   * facebook_uuid -> CHAR(36) (uuid stored as text via Go).
--   * CHECK()s from the original schema are kept where MySQL 8 enforces them.
-- All tables are utf8mb4 / InnoDB so the ON DELETE/UPDATE CASCADE FKs work.

CREATE TABLE wmstore_device (
  jid                 VARCHAR(128) PRIMARY KEY,
  lid                 VARCHAR(128) NULL,
  facebook_uuid       CHAR(36)     NULL,
  registration_id     BIGINT       NOT NULL CHECK (registration_id >= 0 AND registration_id < 4294967296),
  noise_key           VARBINARY(32)  NOT NULL,
  identity_key        VARBINARY(32)  NOT NULL,
  signed_pre_key      VARBINARY(32)  NOT NULL,
  signed_pre_key_id   INTEGER        NOT NULL CHECK (signed_pre_key_id >= 0 AND signed_pre_key_id < 16777216),
  signed_pre_key_sig  VARBINARY(64)  NOT NULL,
  adv_key             BLOB           NOT NULL,
  adv_details         BLOB           NOT NULL,
  adv_account_sig     VARBINARY(64)  NOT NULL,
  adv_account_sig_key VARBINARY(32)  NOT NULL,
  adv_device_sig      VARBINARY(64)  NOT NULL,
  platform            VARCHAR(255) NOT NULL DEFAULT '',
  business_name       VARCHAR(255) NOT NULL DEFAULT '',
  push_name           VARCHAR(255) NOT NULL DEFAULT '',
  lid_migration_ts    BIGINT       NOT NULL DEFAULT 0
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_identity_keys (
  our_jid  VARCHAR(128) NOT NULL,
  their_id VARCHAR(128) NOT NULL,
  identity VARBINARY(32) NOT NULL,
  PRIMARY KEY (our_jid, their_id),
  CONSTRAINT fk_wmstore_identity_device FOREIGN KEY (our_jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_pre_keys (
  jid      VARCHAR(128) NOT NULL,
  key_id   INTEGER      NOT NULL CHECK (key_id >= 0 AND key_id < 16777216),
  `key`    VARBINARY(32) NOT NULL,
  uploaded TINYINT(1)   NOT NULL,
  PRIMARY KEY (jid, key_id),
  CONSTRAINT fk_wmstore_prekeys_device FOREIGN KEY (jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_sessions (
  our_jid  VARCHAR(128) NOT NULL,
  their_id VARCHAR(128) NOT NULL,
  session  BLOB NULL,
  PRIMARY KEY (our_jid, their_id),
  CONSTRAINT fk_wmstore_sessions_device FOREIGN KEY (our_jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_sender_keys (
  our_jid    VARCHAR(128) NOT NULL,
  chat_id    VARCHAR(128) NOT NULL,
  sender_id  VARCHAR(128) NOT NULL,
  sender_key BLOB NOT NULL,
  PRIMARY KEY (our_jid, chat_id, sender_id),
  CONSTRAINT fk_wmstore_senderkeys_device FOREIGN KEY (our_jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_app_state_sync_keys (
  jid         VARCHAR(128)  NOT NULL,
  key_id      VARBINARY(255) NOT NULL,
  key_data    BLOB    NOT NULL,
  timestamp   BIGINT  NOT NULL,
  fingerprint BLOB    NOT NULL,
  PRIMARY KEY (jid, key_id),
  CONSTRAINT fk_wmstore_appstatesynckeys_device FOREIGN KEY (jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_app_state_version (
  jid     VARCHAR(128) NOT NULL,
  name    VARCHAR(128) NOT NULL,
  version BIGINT        NOT NULL,
  hash    VARBINARY(128) NOT NULL,
  PRIMARY KEY (jid, name),
  CONSTRAINT fk_wmstore_appstateversion_device FOREIGN KEY (jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_app_state_mutation_macs (
  jid       VARCHAR(128)  NOT NULL,
  name      VARCHAR(128)  NOT NULL,
  version   BIGINT         NOT NULL,
  index_mac VARBINARY(32)  NOT NULL,
  value_mac VARBINARY(32)  NOT NULL,
  PRIMARY KEY (jid, name, version, index_mac),
  CONSTRAINT fk_wmstore_appstatemacs_version FOREIGN KEY (jid, name) REFERENCES wmstore_app_state_version (jid, name) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_contacts (
  our_jid        VARCHAR(128) NOT NULL,
  their_jid      VARCHAR(128) NOT NULL,
  first_name     TEXT NULL,
  full_name      TEXT NULL,
  push_name      TEXT NULL,
  business_name  TEXT NULL,
  redacted_phone TEXT NULL,
  PRIMARY KEY (our_jid, their_jid),
  CONSTRAINT fk_wmstore_contacts_device FOREIGN KEY (our_jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_chat_settings (
  our_jid     VARCHAR(128) NOT NULL,
  chat_jid    VARCHAR(128) NOT NULL,
  muted_until BIGINT     NOT NULL DEFAULT 0,
  pinned      TINYINT(1) NOT NULL DEFAULT 0,
  archived    TINYINT(1) NOT NULL DEFAULT 0,
  PRIMARY KEY (our_jid, chat_jid),
  CONSTRAINT fk_wmstore_chatsettings_device FOREIGN KEY (our_jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_message_secrets (
  our_jid    VARCHAR(128) NOT NULL,
  chat_jid   VARCHAR(128) NOT NULL,
  sender_jid VARCHAR(128) NOT NULL,
  message_id VARCHAR(128) NOT NULL,
  `key`      BLOB NOT NULL,
  PRIMARY KEY (our_jid, chat_jid, sender_jid, message_id),
  CONSTRAINT fk_wmstore_msgsecrets_device FOREIGN KEY (our_jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_privacy_tokens (
  our_jid          VARCHAR(128) NOT NULL,
  their_jid        VARCHAR(128) NOT NULL,
  token            BLOB   NOT NULL,
  timestamp        BIGINT NOT NULL,
  sender_timestamp BIGINT NULL,
  PRIMARY KEY (our_jid, their_jid),
  KEY idx_wmstore_privacy_tokens_our_jid_timestamp (our_jid, timestamp)
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_nct_salt (
  our_jid VARCHAR(128) PRIMARY KEY,
  salt    BLOB NOT NULL,
  CONSTRAINT fk_wmstore_nctsalt_device FOREIGN KEY (our_jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_lid_map (
  lid VARCHAR(128) PRIMARY KEY,
  pn  VARCHAR(128) NOT NULL,
  UNIQUE KEY uq_wmstore_lid_map_pn (pn)
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_event_buffer (
  our_jid          VARCHAR(128)  NOT NULL,
  ciphertext_hash  VARBINARY(32) NOT NULL,
  plaintext        BLOB NULL,
  server_timestamp BIGINT NOT NULL,
  insert_timestamp BIGINT NOT NULL,
  PRIMARY KEY (our_jid, ciphertext_hash),
  CONSTRAINT fk_wmstore_eventbuffer_device FOREIGN KEY (our_jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE wmstore_retry_buffer (
  our_jid    VARCHAR(128) NOT NULL,
  chat_jid   VARCHAR(128) NOT NULL,
  message_id VARCHAR(128) NOT NULL,
  format     VARCHAR(255) NOT NULL,
  plaintext  BLOB   NOT NULL,
  timestamp  BIGINT NOT NULL,
  PRIMARY KEY (our_jid, chat_jid, message_id),
  KEY idx_wmstore_retry_buffer_timestamp (our_jid, timestamp),
  CONSTRAINT fk_wmstore_retrybuffer_device FOREIGN KEY (our_jid) REFERENCES wmstore_device (jid) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
