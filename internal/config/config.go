// Package config loads, defaults, and validates the gateway's runtime
// configuration from environment variables (see masterplan §12). A .env file is
// loaded on boot when present (handy for local dev) and ignored otherwise.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
)

// GatewayConfig is the fully-parsed gateway runtime configuration. Every field maps to an ENV
// var documented in masterplan §12.
type GatewayConfig struct {
	// HTTP / server
	HTTPAddr  string // GATEWAY_HTTP_ADDR; deprecated fallback HTTP_ADDR
	PublicURL string // GATEWAY_PUBLIC_URL; deprecated fallback PUBLIC_URL

	// Secrets
	AppEncryptionKey string // APP_ENCRYPTION_KEY (base64 32-byte AES-GCM key)

	// Gateway identity (session pinning, gateways registry — §4.5)
	GatewayID        string // GATEWAY_ID
	ControlPlaneAddr string // GATEWAY_CONTROL_PLANE_ADDR; empty disables private gRPC bootstrap
	CredentialDir    string // GATEWAY_CREDENTIAL_DIR
	BootstrapCAFile  string // GATEWAY_BOOTSTRAP_CA_FILE
	EnrollmentToken  string // GATEWAY_ENROLLMENT_TOKEN; bootstrap-only, never persisted

	// Trust model (§4.1/§4.4). After the central-router cutover the gateway no
	// longer verifies end-user JWTs/api-keys directly: the router authenticates
	// callers and vouches a resolved Principal via a request-bound Ed25519
	// assertion. The gateway verifies that assertion against the router's JWKS
	// (docs/specs/router.md, plan D3) — these are the gateway's trust inputs.
	RouterJWKSURL         string // ROUTER_JWKS_URL: the router's public JWKS (assertion verify)
	RouterAssertionIssuer string // ROUTER_ASSERTION_ISSUER: expected `iss` on assertions (default "router")

	// Legacy better-auth inputs (still consumed in single-binary/dev fallbacks and
	// kept for the trust-seam contract tests). The router is the primary consumer.
	BetterAuthURL     string   // BETTER_AUTH_URL: frontend base URL; the JWT iss/aud to enforce
	BetterAuthJWKSURL string   // BETTER_AUTH_JWKS_URL: defaults to ${BETTER_AUTH_URL}/api/auth/jwks
	FrontendOrigins   []string // FRONTEND_ORIGINS: comma-list of allowed CORS origins

	// App data store
	MySQLDSN string // MYSQL_DSN

	// whatsmeow keystore — always SQLite in v2 (§6.1); the DSN points at the
	// gateway-local pure-Go SQLite file. No driver selection any more.
	WhatsmeowStoreDSN string // WHATSMEOW_STORE_DSN

	// Infra
	RedisURL string // REDIS_URL

	// Control bus (§4.6) — cross-service ctrl:* pub/sub (key/user revocation).
	PubSubRedisURL string // PUBSUB_REDIS_URL: defaults to REDIS_URL (single instance)
	RedisPrefix    string // REDIS_PREFIX: isolates independent stacks on one Redis (default "gw")

	// Admin bootstrap (§8): the better-auth user that owns the admin session,
	// when known. Empty => system-owned admin session (sentinel org).
	GatewayAdminUserID string // GATEWAY_ADMIN_USER_ID

	// Admin WhatsApp number
	WhatsAppAdminNumber    string // WHATSAPP_ADMIN_NUMBER
	WhatsAppAdminCmdPrefix string // WHATSAPP_ADMIN_CMD_PREFIX
	// WhatsAppAdminOrgID is the organization the bootstrapped admin session
	// belongs to. It must be a real organization in better-auth's `organization`
	// table; otherwise the admin session's events cannot publish (they are
	// org-keyed) and the boot orphan-guard marks the session STOPPED on the next
	// restart. Required whenever WHATSAPP_ADMIN_NUMBER is set.
	WhatsAppAdminOrgID string // WHATSAPP_ADMIN_ORG_ID
	// WhatsAppDeviceName overrides the OS/app label WhatsApp shows for newly
	// linked companion devices. When empty, the manager derives "Linux -
	// <GATEWAY_ID>" so Linked devices does not expose the whatsmeow library.
	WhatsAppDeviceName string // WHATSAPP_DEVICE_NAME

	// Per-session defaults
	DefaultRatePerMin  int  // DEFAULT_RATE_PER_MIN
	DefaultRatePerHour int  // DEFAULT_RATE_PER_HOUR
	DefaultAutoRead    bool // DEFAULT_AUTO_READ

	// Source-level ignore rules
	IgnoreStatus    bool // IGNORE_STATUS
	IgnoreGroups    bool // IGNORE_GROUPS
	IgnoreChannels  bool // IGNORE_CHANNELS
	IgnoreBroadcast bool // IGNORE_BROADCAST

	// Global webhook defaults
	WebhookURL           string   // WEBHOOK_URL
	WebhookEvents        []string // WEBHOOK_EVENTS (comma-separated)
	WebhookHMACKey       string   // WEBHOOK_HMAC_KEY
	WebhookRetryDelay    int      // WEBHOOK_RETRIES_DELAY (seconds)
	WebhookRetryAttempts int      // WEBHOOK_RETRIES_ATTEMPTS

	// Data retention
	RetentionDays int // RETENTION_DAYS (0 = keep forever)

	// Observability
	LogLevel string // LOG_LEVEL
}

// LoadGateway reads gateway configuration from the environment, applying defaults from
// masterplan §12. It loads a .env file first if one exists in the working
// directory (a no-op when absent, so production can inject real env vars).
func LoadGateway() (*GatewayConfig, error) {
	// Best-effort .env load; ignore "not found" so prod is unaffected. The
	// gateway env file lives at deploy/.env (the same file the Docker dev
	// profiles read); ".env" at the repo root is kept as a fallback. godotenv
	// does not override vars already set in the environment, so a container's
	// injected env always wins over these files.
	_ = godotenv.Load("deploy/.env", ".env")

	cfg := &GatewayConfig{
		HTTPAddr:               getStringFallback("GATEWAY_HTTP_ADDR", "HTTP_ADDR", ":8080"),
		PublicURL:              getStringFallback("GATEWAY_PUBLIC_URL", "PUBLIC_URL", ""),
		GatewayID:              getString("GATEWAY_ID", "gw-1"),
		ControlPlaneAddr:       getString("GATEWAY_CONTROL_PLANE_ADDR", ""),
		CredentialDir:          getString("GATEWAY_CREDENTIAL_DIR", ""),
		BootstrapCAFile:        getString("GATEWAY_BOOTSTRAP_CA_FILE", ""),
		EnrollmentToken:        getString("GATEWAY_ENROLLMENT_TOKEN", ""),
		RouterJWKSURL:          getString("ROUTER_JWKS_URL", ""),
		RouterAssertionIssuer:  getString("ROUTER_ASSERTION_ISSUER", DefaultRouterIssuer),
		BetterAuthURL:          getString("BETTER_AUTH_URL", ""),
		BetterAuthJWKSURL:      getString("BETTER_AUTH_JWKS_URL", ""),
		FrontendOrigins:        getCSV("FRONTEND_ORIGINS"),
		AppEncryptionKey:       getString("APP_ENCRYPTION_KEY", ""),
		MySQLDSN:               getString("MYSQL_DSN", ""),
		WhatsmeowStoreDSN:      getString("WHATSMEOW_STORE_DSN", "file:store.db?_foreign_keys=on"),
		RedisURL:               getString("REDIS_URL", ""),
		PubSubRedisURL:         getString("PUBSUB_REDIS_URL", ""),
		RedisPrefix:            getString("REDIS_PREFIX", "gw"),
		GatewayAdminUserID:     getString("GATEWAY_ADMIN_USER_ID", ""),
		WhatsAppAdminNumber:    getString("WHATSAPP_ADMIN_NUMBER", ""),
		WhatsAppAdminCmdPrefix: getString("WHATSAPP_ADMIN_CMD_PREFIX", "am"),
		WhatsAppAdminOrgID:     getString("WHATSAPP_ADMIN_ORG_ID", ""),
		WhatsAppDeviceName:     getString("WHATSAPP_DEVICE_NAME", ""),
		DefaultRatePerMin:      getInt("DEFAULT_RATE_PER_MIN", 20),
		DefaultRatePerHour:     getInt("DEFAULT_RATE_PER_HOUR", 200),
		DefaultAutoRead:        getBool("DEFAULT_AUTO_READ", true),
		IgnoreStatus:           getBool("IGNORE_STATUS", false),
		IgnoreGroups:           getBool("IGNORE_GROUPS", false),
		IgnoreChannels:         getBool("IGNORE_CHANNELS", false),
		IgnoreBroadcast:        getBool("IGNORE_BROADCAST", false),
		WebhookURL:             getString("WEBHOOK_URL", ""),
		WebhookEvents:          getCSV("WEBHOOK_EVENTS"),
		WebhookHMACKey:         getString("WEBHOOK_HMAC_KEY", ""),
		WebhookRetryDelay:      getInt("WEBHOOK_RETRIES_DELAY", 2),
		WebhookRetryAttempts:   getInt("WEBHOOK_RETRIES_ATTEMPTS", 15),
		RetentionDays:          getInt("RETENTION_DAYS", 0),
		LogLevel:               getString("LOG_LEVEL", "info"),
	}

	// BETTER_AUTH_JWKS_URL defaults to ${BETTER_AUTH_URL}/api/auth/jwks (§4.1, §14).
	if cfg.BetterAuthJWKSURL == "" && cfg.BetterAuthURL != "" {
		cfg.BetterAuthJWKSURL = strings.TrimRight(cfg.BetterAuthURL, "/") + "/api/auth/jwks"
	}

	// PUBSUB_REDIS_URL (control bus) defaults to REDIS_URL — single-instance dev
	// collapses both roles onto one Redis (§4.6, §14).
	if cfg.PubSubRedisURL == "" {
		cfg.PubSubRedisURL = cfg.RedisURL
	}

	return cfg, nil
}

// Validate checks invariants that must hold before the server starts. It is
// intentionally lenient about secrets that are only required by features filled
// in by later milestones; those subsystems validate their own prerequisites.
func (c *GatewayConfig) Validate() error {
	if c.HTTPAddr == "" {
		return fmt.Errorf("config: GATEWAY_HTTP_ADDR must not be empty")
	}

	// The whatsmeow keystore is always gateway-local SQLite in v2 (§6.1).
	if c.WhatsmeowStoreDSN == "" {
		return fmt.Errorf("config: WHATSMEOW_STORE_DSN must not be empty")
	}

	if c.GatewayID == "" {
		return fmt.Errorf("config: GATEWAY_ID must not be empty")
	}
	privateConfigured := c.ControlPlaneAddr != "" || c.CredentialDir != "" || c.BootstrapCAFile != "" || c.EnrollmentToken != ""
	if privateConfigured {
		if c.ControlPlaneAddr == "" || c.CredentialDir == "" || c.BootstrapCAFile == "" {
			return fmt.Errorf("config: GATEWAY_CONTROL_PLANE_ADDR, GATEWAY_CREDENTIAL_DIR, and GATEWAY_BOOTSTRAP_CA_FILE must be configured together")
		}
		if pki.ValidateGatewayID(c.GatewayID) != nil {
			return fmt.Errorf("config: GATEWAY_ID must be canonical for private control plane")
		}
		if strings.TrimSpace(c.ControlPlaneAddr) != c.ControlPlaneAddr {
			return fmt.Errorf("config: GATEWAY_CONTROL_PLANE_ADDR must be canonical")
		}
		for name, value := range map[string]string{"GATEWAY_CREDENTIAL_DIR": c.CredentialDir, "GATEWAY_BOOTSTRAP_CA_FILE": c.BootstrapCAFile} {
			if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) {
				return fmt.Errorf("config: %s must be an absolute clean non-root path", name)
			}
		}
	}

	// A configured admin number must name the organization that owns its session.
	// Without it the admin session's (org-keyed) events cannot publish and the boot
	// orphan-guard would stop the session on the next restart.
	if c.WhatsAppAdminNumber != "" && c.WhatsAppAdminOrgID == "" {
		return fmt.Errorf("config: WHATSAPP_ADMIN_ORG_ID must be set when WHATSAPP_ADMIN_NUMBER is set")
	}

	if c.DefaultRatePerMin < 0 || c.DefaultRatePerHour < 0 {
		return fmt.Errorf("config: default rate limits must be non-negative")
	}
	if c.RetentionDays < 0 {
		return fmt.Errorf("config: RETENTION_DAYS must be non-negative")
	}

	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: LOG_LEVEL must be one of debug|info|warn|error, got %q", c.LogLevel)
	}

	return nil
}

// --- parse helpers ---

func getString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getStringFallback(primary, fallback, def string) string {
	if v := getString(primary, ""); v != "" {
		return v
	}
	return getString(fallback, def)
}

func getInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func getBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return def
}

func getCSV(key string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
