-- 0007_oidc_provider: Sign in with WhatsApp OAuth 2.1 / OIDC provider state.

CREATE TABLE oauth_clients (
  id                  VARCHAR(64) PRIMARY KEY,
  client_id           VARCHAR(64) NOT NULL,
  organization_id     VARCHAR(64) NOT NULL,
  created_by_user_id  VARCHAR(64) NULL,
  session_id          VARCHAR(64) NOT NULL,
  name                VARCHAR(255) NOT NULL,
  bot_name            VARCHAR(255) NULL,
  logo_url            TEXT NULL,
  client_type         ENUM('confidential','public') NOT NULL DEFAULT 'confidential',
  login_command       VARCHAR(32) NOT NULL DEFAULT 'login',
  secret_hash         VARBINARY(64) NULL,
  secret_last4        VARCHAR(8) NULL,
  redirect_uris       JSON NOT NULL,
  modes               SET('dm','group') NOT NULL DEFAULT 'dm',
  group_jid           VARCHAR(255) NULL,
  allowed_scopes      JSON NOT NULL,
  token_ttl_seconds   INT NOT NULL DEFAULT 900,
  refresh_ttl_seconds INT NOT NULL DEFAULT 2592000,
  status              ENUM('active','disabled') NOT NULL DEFAULT 'active',
  created_at          BIGINT NOT NULL,
  updated_at          BIGINT NOT NULL,
  deleted_at          BIGINT NULL,
  UNIQUE KEY uq_oauth_client_id (client_id),
  KEY idx_oauth_clients_org (organization_id),
  KEY idx_oauth_clients_session (session_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE oauth_grants (
  id               VARCHAR(64) PRIMARY KEY,
  organization_id  VARCHAR(64) NOT NULL,
  client_id        VARCHAR(64) NOT NULL,
  wa_identity_id   BIGINT UNSIGNED NOT NULL,
  sub              VARCHAR(80) NOT NULL,
  granted_scopes   JSON NOT NULL,
  last_acr         VARCHAR(16) NOT NULL,
  last_group_jid   VARCHAR(255) NULL,
  created_at       BIGINT NOT NULL,
  last_used_at     BIGINT NOT NULL,
  revoked_at       BIGINT NULL,
  UNIQUE KEY uq_grant_client_identity (client_id, wa_identity_id),
  KEY idx_grants_org_client (organization_id, client_id),
  KEY idx_grants_sub (sub)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE oauth_refresh_tokens (
  id               VARCHAR(64) PRIMARY KEY,
  grant_id         VARCHAR(64) NOT NULL,
  organization_id  VARCHAR(64) NOT NULL,
  token_hash       VARBINARY(64) NOT NULL,
  family_id        VARCHAR(64) NOT NULL,
  parent_id        VARCHAR(64) NULL,
  scopes           JSON NOT NULL,
  issued_at        BIGINT NOT NULL,
  expires_at       BIGINT NOT NULL,
  consumed_at      BIGINT NULL,
  revoked_at       BIGINT NULL,
  UNIQUE KEY uq_refresh_hash (token_hash),
  KEY idx_refresh_grant (grant_id),
  KEY idx_refresh_family (family_id),
  KEY idx_refresh_org (organization_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE oauth_signing_keys (
  kid          VARCHAR(64) PRIMARY KEY,
  alg          VARCHAR(16) NOT NULL DEFAULT 'EdDSA',
  public_jwk   JSON NOT NULL,
  private_enc  VARBINARY(4096) NOT NULL,
  status       ENUM('active','next','retired') NOT NULL,
  created_at   BIGINT NOT NULL,
  retired_at   BIGINT NULL
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
