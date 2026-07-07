package config

import (
	"fmt"
	"strings"

	"github.com/joho/godotenv"
)

// DefaultRouterIssuer is the assertion `iss` both the router (minter) and the
// gateway (verifier) default to, so a single deployment needs no extra knob to
// agree on the router's identity. Override with ROUTER_ISSUER / ROUTER_ASSERTION_ISSUER.
const DefaultRouterIssuer = "router"

// RouterConfig is the central router's runtime configuration (docs/specs/router.md).
// The router is the system's single trust boundary: it authenticates callers
// against cached better-auth JWKS + the shared `apikey` table, resolves the owning
// gateway for each session, and proxies the request under a signed internal
// assertion. It needs the shared MySQL (routing table), one Redis (control bus +,
// later, realtime), the better-auth JWKS inputs, and its own Ed25519 signing key.
type RouterConfig struct {
	// HTTP / server
	HTTPAddr  string // ROUTER_HTTP_ADDR (default :8090)
	PublicURL string // ROUTER_PUBLIC_URL (advertised base; realtime WS url is derived from it)

	// Trust boundary — authn inputs (same better-auth JWKS the gateway used to use).
	BetterAuthURL     string   // BETTER_AUTH_URL: JWT iss/aud to enforce
	BetterAuthJWKSURL string   // BETTER_AUTH_JWKS_URL: defaults to ${BETTER_AUTH_URL}/api/auth/jwks
	FrontendOrigins   []string // FRONTEND_ORIGINS: allowed browser CORS origins

	// Internal assertion (router→gateway). The router holds the private key and
	// publishes the public JWKS at /.well-known/router-jwks.json.
	Ed25519PrivateKey string // ROUTER_ED25519_PRIVATE_KEY (base64 seed or full key)
	Issuer            string // ROUTER_ISSUER: assertion `iss` (default DefaultRouterIssuer)

	// Shared data + infra.
	MySQLDSN       string // MYSQL_DSN (the routing table: wa_sessions + gateways)
	RedisURL       string // REDIS_URL
	PubSubRedisURL string // PUBSUB_REDIS_URL: control bus; defaults to REDIS_URL
	RedisPrefix    string // REDIS_PREFIX: isolates stacks (default "gw")

	// OIDC provider.
	OIDCIssuer              string // OIDC_ISSUER: defaults to ROUTER_PUBLIC_URL
	OIDCKeyEncKey           string // OIDC_KEY_ENC_KEY: base64/raw 32-byte AES-GCM key
	OIDCPairwiseSalt        string // OIDC_PAIRWISE_SALT: HMAC key for pairwise subjects
	OAuthClientSecretPepper string // OAUTH_CLIENT_SECRET_PEPPER: pepper for SHA-256(client_secret+pepper)
	WhatsAppAdminCmdPrefix  string // WHATSAPP_ADMIN_CMD_PREFIX: reserved command namespace prefix
	WebLoginURL             string // WEB_LOGIN_URL: public consent page URL
	OIDCRequestTTLSeconds   int    // OIDC_REQUEST_TTL_SECONDS
	OIDCAuthCodeTTLSeconds  int    // OIDC_AUTHCODE_TTL_SECONDS

	// Observability
	LogLevel string // LOG_LEVEL
}

// LoadRouter reads the router configuration from the environment, mirroring the
// gateway's Load (it reads the same deploy/.env so a single file configures both).
func LoadRouter() (*RouterConfig, error) {
	_ = godotenv.Load("deploy/.env", ".env")

	cfg := &RouterConfig{
		HTTPAddr:                getString("ROUTER_HTTP_ADDR", ":8090"),
		PublicURL:               getString("ROUTER_PUBLIC_URL", ""),
		BetterAuthURL:           getString("BETTER_AUTH_URL", ""),
		BetterAuthJWKSURL:       getString("BETTER_AUTH_JWKS_URL", ""),
		FrontendOrigins:         getCSV("FRONTEND_ORIGINS"),
		Ed25519PrivateKey:       getString("ROUTER_ED25519_PRIVATE_KEY", ""),
		Issuer:                  getString("ROUTER_ISSUER", DefaultRouterIssuer),
		MySQLDSN:                getString("MYSQL_DSN", ""),
		RedisURL:                getString("REDIS_URL", ""),
		PubSubRedisURL:          getString("PUBSUB_REDIS_URL", ""),
		RedisPrefix:             getString("REDIS_PREFIX", "gw"),
		OIDCIssuer:              getString("OIDC_ISSUER", ""),
		OIDCKeyEncKey:           getString("OIDC_KEY_ENC_KEY", ""),
		OIDCPairwiseSalt:        getString("OIDC_PAIRWISE_SALT", ""),
		OAuthClientSecretPepper: getString("OAUTH_CLIENT_SECRET_PEPPER", ""),
		WhatsAppAdminCmdPrefix:  getString("WHATSAPP_ADMIN_CMD_PREFIX", "am"),
		WebLoginURL:             getString("WEB_LOGIN_URL", ""),
		OIDCRequestTTLSeconds:   getInt("OIDC_REQUEST_TTL_SECONDS", 600),
		OIDCAuthCodeTTLSeconds:  getInt("OIDC_AUTHCODE_TTL_SECONDS", 60),
		LogLevel:                getString("LOG_LEVEL", "info"),
	}
	if cfg.OIDCIssuer == "" {
		cfg.OIDCIssuer = cfg.PublicURL
	}

	if cfg.BetterAuthJWKSURL == "" && cfg.BetterAuthURL != "" {
		cfg.BetterAuthJWKSURL = strings.TrimRight(cfg.BetterAuthURL, "/") + "/api/auth/jwks"
	}
	if cfg.PubSubRedisURL == "" {
		cfg.PubSubRedisURL = cfg.RedisURL
	}
	if cfg.WebLoginURL == "" {
		cfg.WebLoginURL = strings.TrimRight(getString("WEB_URL", ""), "/") + "/login/whatsapp"
	}
	return cfg, nil
}

// Validate checks the router's hard prerequisites. Unlike the gateway it cannot
// start without its trust inputs: without the signing key it cannot mint
// assertions, and without the better-auth JWKS it cannot authenticate anyone.
func (c *RouterConfig) Validate() error {
	if c.HTTPAddr == "" {
		return fmt.Errorf("config: ROUTER_HTTP_ADDR must not be empty")
	}
	if c.Ed25519PrivateKey == "" {
		return fmt.Errorf("config: ROUTER_ED25519_PRIVATE_KEY is required (the router signs internal assertions)")
	}
	if c.Issuer == "" {
		return fmt.Errorf("config: ROUTER_ISSUER must not be empty")
	}
	if c.MySQLDSN == "" {
		return fmt.Errorf("config: MYSQL_DSN is required")
	}
	if c.BetterAuthURL == "" || c.BetterAuthJWKSURL == "" {
		return fmt.Errorf("config: BETTER_AUTH_URL (and JWKS) are required for the router to authenticate callers")
	}
	if c.PublicURL == "" {
		return fmt.Errorf("config: ROUTER_PUBLIC_URL is required")
	}
	if c.OIDCIssuer == "" {
		return fmt.Errorf("config: OIDC_ISSUER or ROUTER_PUBLIC_URL is required")
	}
	if c.OIDCKeyEncKey == "" {
		return fmt.Errorf("config: OIDC_KEY_ENC_KEY is required for OIDC signing keys")
	}
	if c.OIDCPairwiseSalt == "" {
		return fmt.Errorf("config: OIDC_PAIRWISE_SALT is required")
	}
	if c.OAuthClientSecretPepper == "" {
		return fmt.Errorf("config: OAUTH_CLIENT_SECRET_PEPPER is required for OAuth client secret hashing")
	}
	if c.RedisURL == "" {
		return fmt.Errorf("config: REDIS_URL is required for OAuth pending requests")
	}
	if c.WebLoginURL == "" || c.WebLoginURL == "/login/whatsapp" {
		return fmt.Errorf("config: WEB_LOGIN_URL is required for OAuth consent redirects")
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: LOG_LEVEL must be one of debug|info|warn|error, got %q", c.LogLevel)
	}
	return nil
}
