-- Content is shared globally; messages retain only references to these bytes.
CREATE TABLE sticker_blobs (
  sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  content LONGBLOB NOT NULL
);
CREATE TABLE sticker_messages (
  session_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL,
  chat_jid VARCHAR(255) NOT NULL,
  message_id VARCHAR(255) NOT NULL,
  sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  PRIMARY KEY (session_id, chat_jid, message_id),
  FOREIGN KEY (sha256) REFERENCES sticker_blobs(sha256),
  FOREIGN KEY (session_id) REFERENCES wa_sessions(id) ON DELETE CASCADE
);
