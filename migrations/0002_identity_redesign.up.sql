-- Identity model redesign (R-cleanup).
--
-- One central identity table (whatsapp_identities, keyed by canonical LID), a
-- group-membership PIVOT (whatsapp_group_members) that links an identity to a
-- group with a role + a per-group member `tag` (the second per-group identity
-- WhatsApp shows beside the push name), and DM contacts derived from the per-
-- session `chats` table. The per-session whatsapp_contacts table is removed.
--
-- The identity/group/membership data is wiped so a re-backfill repopulates it in
-- the canonical (non-AD LID) form — the old rows were inconsistent (device-suffix
-- duplicates, phone-JID-keyed identities, names polluted with the key).

-- DM "found" status is now derived from chats (type='dm'); drop the per-session
-- contact table entirely.
DROP TABLE IF EXISTS whatsapp_contacts;

-- Recreate the membership pivot with `tag` replacing `group_nickname`.
DROP TABLE IF EXISTS whatsapp_group_members;
CREATE TABLE whatsapp_group_members (
  id             BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  session_id     VARCHAR(64) NOT NULL,               -- the account that observed the membership
  group_jid      VARCHAR(255) NOT NULL,              -- -> whatsapp_groups.group_jid
  lid            VARCHAR(255) NOT NULL,              -- -> whatsapp_identities.lid (canonical)
  tag            TEXT NULL,                          -- per-group member tag (WhatsApp DisplayName)
  role           ENUM('member','admin','superadmin') NOT NULL DEFAULT 'member',
  first_seen_at  BIGINT NOT NULL,
  last_seen_at   BIGINT NOT NULL,
  UNIQUE KEY uq_group_member (session_id, group_jid, lid),
  KEY idx_gm_group (group_jid),
  KEY idx_gm_lid (lid)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Wipe identities + groups so the next backfill rebuilds them cleanly.
TRUNCATE TABLE whatsapp_identities;
TRUNCATE TABLE whatsapp_groups;
