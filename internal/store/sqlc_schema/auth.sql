-- sqlc-only schema for frontend-owned Better Auth tables read by the gateway.
-- These definitions are not gateway migrations. The frontend/drizzle toolchain
-- remains the sole owner of these tables; this file only lets sqlc type-check
-- read-only gateway queries against the shared auth schema.

CREATE TABLE apikey (
  id           VARCHAR(64) PRIMARY KEY,
  name         TEXT NULL,
  `key`        VARCHAR(255) NOT NULL,
  reference_id VARCHAR(255) NULL,
  enabled      TINYINT(1) NULL,
  expires_at   TIMESTAMP(3) NULL,
  permissions  TEXT NULL,
  created_at   TIMESTAMP(3) NULL,
  last_request TIMESTAMP(3) NULL
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE organization (
  id VARCHAR(64) PRIMARY KEY
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
