CREATE TABLE media_buckets (
 id VARCHAR(26) PRIMARY KEY,
 organization_id VARCHAR(191) NOT NULL,
 name VARCHAR(255) NOT NULL,
 endpoint TEXT NOT NULL,
 region VARCHAR(255) NOT NULL,
 bucket VARCHAR(255) NOT NULL,
 path_style BOOLEAN NOT NULL,
 credentials LONGBLOB NOT NULL,
 retention_days BIGINT NULL,
 created_at BIGINT NOT NULL,
 INDEX media_buckets_org (organization_id)
);
CREATE TABLE session_media_storage (
 session_id VARCHAR(64) PRIMARY KEY,
 organization_id VARCHAR(191) NOT NULL,
 bucket_id VARCHAR(26) NOT NULL,
 FOREIGN KEY (bucket_id) REFERENCES media_buckets(id),
 INDEX session_media_storage_org (organization_id)
);
CREATE TABLE media_assets (
 id VARCHAR(26) PRIMARY KEY,
 organization_id VARCHAR(191) NOT NULL,
 session_id VARCHAR(64) NOT NULL,
 message_id VARCHAR(255) NOT NULL,
 attachment_index INT NOT NULL DEFAULT 0,
 bucket_id VARCHAR(26) NOT NULL,
 object_key VARCHAR(512) NOT NULL,
 version_id TEXT NULL,
 access_token VARCHAR(64) NOT NULL,
 source LONGBLOB NULL,
 mimetype VARCHAR(255) NOT NULL,
 filename TEXT NOT NULL,
 size BIGINT NOT NULL DEFAULT 0,
 status VARCHAR(16) NOT NULL DEFAULT 'pending',
 created_at BIGINT NOT NULL,
 expires_at BIGINT NULL,
 next_attempt_at BIGINT NOT NULL,
 attempts BIGINT NOT NULL DEFAULT 0,
 last_error TEXT NULL,
 notification JSON NULL,
 FOREIGN KEY (bucket_id) REFERENCES media_buckets(id),
 UNIQUE KEY media_asset_message (organization_id, session_id, message_id, attachment_index),
 INDEX media_work_due (next_attempt_at, id),
 INDEX media_expiry_due (status, expires_at),
 INDEX media_bucket_assets (bucket_id, status)
);
