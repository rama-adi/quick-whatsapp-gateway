package config

import (
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// APIConfig is the API/control plane runtime configuration. The API remains the
// system's single trust boundary: it authenticates callers against cached
// better-auth JWKS + the shared `apikey` table and serves every public
// operation locally over REST/gRPC. It needs the shared MySQL, one Redis
// (control bus + realtime), and the better-auth JWKS inputs.
type APIConfig struct {
	// HTTP / server
	HTTPAddr                   string        // API_HTTP_ADDR (default :8090)
	PublicGRPCAddr             string        // API_PUBLIC_GRPC_ADDR: plaintext local/trusted ingress hop (default :8081)
	GatewayGRPCAddr            string        // API_GATEWAY_GRPC_ADDR: private mTLS listener; empty disables it
	GatewayTLSIdentityDir      string        // API_GATEWAY_TLS_IDENTITY_DIR
	GatewayTLSRenewBefore      time.Duration // API_GATEWAY_TLS_RENEW_BEFORE
	GatewayEngineUnaryDeadline time.Duration // API_GATEWAY_ENGINE_UNARY_DEADLINE; required when private engine is enabled
	GatewayEngineSendDeadline  time.Duration // API_GATEWAY_ENGINE_SEND_DEADLINE; required when private engine is enabled
	GatewayPKI                 *PKIConfig    // loaded only when the private listener is enabled
	PublicURL                  string        // API_PUBLIC_URL

	// Trust boundary — authn inputs (same better-auth JWKS the gateway used to use).
	BetterAuthURL     string   // BETTER_AUTH_URL: JWT iss/aud to enforce
	BetterAuthJWKSURL string   // BETTER_AUTH_JWKS_URL: defaults to ${BETTER_AUTH_URL}/api/auth/jwks
	FrontendOrigins   []string // FRONTEND_ORIGINS: allowed browser CORS origins

	// Shared data + infra.
	MySQLDSN       string // MYSQL_DSN (the routing table: wa_sessions + gateways)
	RedisURL       string // REDIS_URL
	PubSubRedisURL string // PUBSUB_REDIS_URL: control bus; defaults to REDIS_URL
	RedisPrefix    string // REDIS_PREFIX: isolates stacks (default "gw")

	// OIDC provider.
	OIDCIssuer              string // OIDC_ISSUER: defaults to API_PUBLIC_URL
	OIDCKeyEncKey           string // OIDC_KEY_ENC_KEY: base64/raw 32-byte AES-GCM key
	OAuthClientSecretPepper string // OAUTH_CLIENT_SECRET_PEPPER: pepper for SHA-256(client_secret+pepper)
	AppEncryptionKey        string // APP_ENCRYPTION_KEY: AES-GCM key for webhook HMAC secrets (API-owned since Increment 7)
	WebhookRetryDelay       int    // WEBHOOK_RETRIES_DELAY (seconds): seeds new webhooks' retry policy
	WebhookRetryAttempts    int    // WEBHOOK_RETRIES_ATTEMPTS: seeds new webhooks' retry policy
	WhatsAppAdminCmdPrefix  string // WHATSAPP_ADMIN_CMD_PREFIX: reserved command namespace prefix
	WebLoginURL             string // WEB_LOGIN_URL: public consent page URL
	OIDCRequestTTLSeconds   int    // OIDC_REQUEST_TTL_SECONDS
	OIDCAuthCodeTTLSeconds  int    // OIDC_AUTHCODE_TTL_SECONDS
	OIDCTrustProxy          bool   // OIDC_TRUST_PROXY: honor X-Forwarded-For for OAuth stream caps

	// Observability
	LogLevel string // LOG_LEVEL
}

// LoadAPI reads API/control-plane configuration from the environment.
func LoadAPI() (*APIConfig, error) {
	_ = godotenv.Load("deploy/.env", ".env")

	cfg := &APIConfig{
		HTTPAddr:                   getString("API_HTTP_ADDR", ":8090"),
		PublicGRPCAddr:             getString("API_PUBLIC_GRPC_ADDR", ":8081"),
		GatewayGRPCAddr:            getString("API_GATEWAY_GRPC_ADDR", ""),
		GatewayTLSIdentityDir:      getString("API_GATEWAY_TLS_IDENTITY_DIR", ""),
		GatewayEngineUnaryDeadline: 0,
		PublicURL:                  getString("API_PUBLIC_URL", ""),
		BetterAuthURL:              getString("BETTER_AUTH_URL", ""),
		BetterAuthJWKSURL:          getString("BETTER_AUTH_JWKS_URL", ""),
		FrontendOrigins:            getCSV("FRONTEND_ORIGINS"),
		MySQLDSN:                   getString("MYSQL_DSN", ""),
		RedisURL:                   getString("REDIS_URL", ""),
		PubSubRedisURL:             getString("PUBSUB_REDIS_URL", ""),
		RedisPrefix:                getString("REDIS_PREFIX", "gw"),
		OIDCIssuer:                 getString("OIDC_ISSUER", ""),
		OIDCKeyEncKey:              getString("OIDC_KEY_ENC_KEY", ""),
		OAuthClientSecretPepper:    getString("OAUTH_CLIENT_SECRET_PEPPER", ""),
		AppEncryptionKey:           getString("APP_ENCRYPTION_KEY", ""),
		WebhookRetryDelay:          getInt("WEBHOOK_RETRIES_DELAY", 2),
		WebhookRetryAttempts:       getInt("WEBHOOK_RETRIES_ATTEMPTS", 5),
		WhatsAppAdminCmdPrefix:     getString("WHATSAPP_ADMIN_CMD_PREFIX", "am"),
		WebLoginURL:                getString("WEB_LOGIN_URL", ""),
		OIDCRequestTTLSeconds:      getInt("OIDC_REQUEST_TTL_SECONDS", 600),
		OIDCAuthCodeTTLSeconds:     getInt("OIDC_AUTHCODE_TTL_SECONDS", 60),
		OIDCTrustProxy:             getBool("OIDC_TRUST_PROXY", false),
		LogLevel:                   getString("LOG_LEVEL", "info"),
	}
	if value := getString("API_GATEWAY_TLS_RENEW_BEFORE", "6h"); value != "" {
		var err error
		cfg.GatewayTLSRenewBefore, err = time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("config: API_GATEWAY_TLS_RENEW_BEFORE: %w", err)
		}
	}
	if cfg.GatewayGRPCAddr != "" {
		value := getString("API_GATEWAY_ENGINE_UNARY_DEADLINE", "")
		if value == "" {
			return nil, fmt.Errorf("config: API_GATEWAY_ENGINE_UNARY_DEADLINE is required")
		}
		duration, parseErr := time.ParseDuration(value)
		if parseErr != nil || duration <= 0 {
			return nil, fmt.Errorf("config: invalid API_GATEWAY_ENGINE_UNARY_DEADLINE")
		}
		cfg.GatewayEngineUnaryDeadline = duration

		// Sends may upload up to 64 MiB of media to WhatsApp before responding;
		// their deadline is separate from (and larger than) unary reads. The
		// default mirrors the outbound worker's 4-minute dispatch budget.
		cfg.GatewayEngineSendDeadline = getDuration("API_GATEWAY_ENGINE_SEND_DEADLINE", 4*time.Minute)
		if cfg.GatewayEngineSendDeadline <= 0 {
			return nil, fmt.Errorf("config: invalid API_GATEWAY_ENGINE_SEND_DEADLINE")
		}
		pkiConfig, err := LoadPKI()
		if err != nil {
			return nil, err
		}
		cfg.GatewayPKI = pkiConfig
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

// Validate checks the API's hard prerequisites. Unlike a gateway it cannot
// start without its trust inputs: without the better-auth JWKS it cannot
// authenticate anyone.
func (c *APIConfig) Validate() error {
	if c.HTTPAddr == "" {
		return fmt.Errorf("config: API_HTTP_ADDR must not be empty")
	}
	if c.PublicGRPCAddr == "" {
		return fmt.Errorf("config: API_PUBLIC_GRPC_ADDR must not be empty")
	}
	overlap, err := tcpEndpointsOverlap(c.HTTPAddr, c.PublicGRPCAddr)
	if err != nil {
		return fmt.Errorf("config: public listen addresses: %w", err)
	}
	if overlap {
		return fmt.Errorf("config: API_PUBLIC_GRPC_ADDR must differ from API_HTTP_ADDR")
	}
	privateConfigured := c.GatewayGRPCAddr != "" || c.GatewayTLSIdentityDir != ""
	if !privateConfigured && c.GatewayPKI != nil {
		return fmt.Errorf("config: private gateway PKI requires API_GATEWAY_GRPC_ADDR")
	}
	if privateConfigured {
		if c.GatewayGRPCAddr == "" || c.GatewayTLSIdentityDir == "" {
			return fmt.Errorf("config: API_GATEWAY_GRPC_ADDR and API_GATEWAY_TLS_IDENTITY_DIR must be configured together")
		}
		if err = validateTLSIdentityDirectory(c.GatewayTLSIdentityDir); err != nil {
			return err
		}
		if c.GatewayTLSRenewBefore <= 0 {
			return fmt.Errorf("config: API_GATEWAY_TLS_RENEW_BEFORE must be positive")
		}
		if c.GatewayPKI == nil {
			return fmt.Errorf("config: private gateway listener requires PKI configuration")
		}
		if err = c.GatewayPKI.Validate(); err != nil {
			return err
		}
		if c.GatewayTLSRenewBefore >= c.GatewayPKI.LeafTTL {
			return fmt.Errorf("config: API_GATEWAY_TLS_RENEW_BEFORE must be shorter than PKI_LEAF_TTL")
		}
		publicListeners := []struct {
			name  string
			value string
		}{
			{name: "API_HTTP_ADDR", value: c.HTTPAddr},
			{name: "API_PUBLIC_GRPC_ADDR", value: c.PublicGRPCAddr},
		}
		for _, publicAddr := range publicListeners {
			overlap, overlapErr := tcpEndpointsOverlap(publicAddr.value, c.GatewayGRPCAddr)
			if overlapErr != nil {
				return fmt.Errorf("config: private listen address against %s: %w", publicAddr.name, overlapErr)
			}
			if overlap {
				return fmt.Errorf("config: API_GATEWAY_GRPC_ADDR must differ from %s", publicAddr.name)
			}
		}
	}
	if c.MySQLDSN == "" {
		return fmt.Errorf("config: MYSQL_DSN is required")
	}
	if c.BetterAuthURL == "" || c.BetterAuthJWKSURL == "" {
		return fmt.Errorf("config: BETTER_AUTH_URL (and JWKS) are required for the router to authenticate callers")
	}
	if c.PublicURL == "" {
		return fmt.Errorf("config: API_PUBLIC_URL is required")
	}
	if c.OIDCIssuer == "" {
		return fmt.Errorf("config: OIDC_ISSUER or API_PUBLIC_URL is required")
	}
	if c.OIDCKeyEncKey == "" {
		return fmt.Errorf("config: OIDC_KEY_ENC_KEY is required for OIDC signing keys")
	}
	if c.OAuthClientSecretPepper == "" {
		return fmt.Errorf("config: OAUTH_CLIENT_SECRET_PEPPER is required for OAuth client secret hashing")
	}
	if c.AppEncryptionKey == "" {
		return fmt.Errorf("config: APP_ENCRYPTION_KEY is required for webhook secret encryption")
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

func validateTLSIdentityDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("config: API_GATEWAY_TLS_IDENTITY_DIR must be an absolute path")
	}
	clean := filepath.Clean(path)
	if clean != path {
		return fmt.Errorf("config: API_GATEWAY_TLS_IDENTITY_DIR must already be clean")
	}
	if filepath.Dir(clean) == clean {
		return fmt.Errorf("config: API_GATEWAY_TLS_IDENTITY_DIR must not be a filesystem root")
	}
	return nil
}

type normalizedBindHost struct {
	wildcard bool
	addrs    map[netip.Addr]struct{}
}

func tcpEndpointsOverlap(left, right string) (bool, error) {
	leftHost, leftPort, err := net.SplitHostPort(left)
	if err != nil {
		return false, fmt.Errorf("invalid API_HTTP_ADDR %q: %w", left, err)
	}
	rightHost, rightPort, err := net.SplitHostPort(right)
	if err != nil {
		return false, fmt.Errorf("invalid API_PUBLIC_GRPC_ADDR %q: %w", right, err)
	}
	if leftPort != rightPort {
		return false, nil
	}
	l, err := normalizeBindHost(leftHost)
	if err != nil {
		return false, fmt.Errorf("API_HTTP_ADDR: %w", err)
	}
	r, err := normalizeBindHost(rightHost)
	if err != nil {
		return false, fmt.Errorf("API_PUBLIC_GRPC_ADDR: %w", err)
	}
	if l.wildcard || r.wildcard {
		return true, nil
	}
	for addr := range l.addrs {
		if _, ok := r.addrs[addr]; ok {
			return true, nil
		}
	}
	return false, nil
}

func normalizeBindHost(host string) (normalizedBindHost, error) {
	if host == "" {
		return normalizedBindHost{wildcard: true}, nil
	}
	if strings.EqualFold(host, "localhost") {
		return normalizedBindHost{addrs: map[netip.Addr]struct{}{
			netip.MustParseAddr("127.0.0.1"): {},
			netip.MustParseAddr("::1"):       {},
		}}, nil
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return normalizedBindHost{}, fmt.Errorf("host %q must be an IP literal, localhost, or wildcard", host)
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() {
		return normalizedBindHost{wildcard: true}, nil
	}
	return normalizedBindHost{addrs: map[netip.Addr]struct{}{addr: {}}}, nil
}
