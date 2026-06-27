-- Reverse the identity redesign: restore the per-session whatsapp_contacts table
-- and the group_nickname column on the membership pivot. (Data is not restored —
-- the up migration wiped it.)

DROP TABLE IF EXISTS whatsapp_group_members;
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

CREATE TABLE whatsapp_contacts (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id    VARCHAR(64) NOT NULL,
  lid           VARCHAR(255) NOT NULL,
  phone         VARCHAR(64)  NULL,
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
