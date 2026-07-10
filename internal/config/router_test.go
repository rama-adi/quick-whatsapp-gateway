package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

// TestLoadRouter_DefaultsAndValidate verifies a complete router environment loads and validates with defaults.
// It isolates environment inputs and compares the loaded values or validation error with the deployment contract.
// This catches configuration drift that could weaken trust assumptions or make startup behavior unpredictable.
func TestLoadRouter_DefaultsAndValidate(t *testing.T) {
	keys := []string{
		"ROUTER_HTTP_ADDR", "ROUTER_PUBLIC_URL", "ROUTER_ISSUER",
		"ROUTER_ED25519_PRIVATE_KEY", "BETTER_AUTH_URL", "BETTER_AUTH_JWKS_URL",
		"FRONTEND_ORIGINS", "MYSQL_DSN", "REDIS_URL", "PUBSUB_REDIS_URL",
		"REDIS_PREFIX", "OIDC_ISSUER", "OIDC_KEY_ENC_KEY", "OAUTH_CLIENT_SECRET_PEPPER",
		"WHATSAPP_ADMIN_CMD_PREFIX", "WEB_LOGIN_URL", "OIDC_REQUEST_TTL_SECONDS",
		"OIDC_AUTHCODE_TTL_SECONDS", "OIDC_TRUST_PROXY", "LOG_LEVEL",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}

	cfg, err := LoadRouter()
	if err != nil {
		t.Fatalf("LoadRouter: %v", err)
	}
	if cfg.HTTPAddr != ":8090" {
		t.Errorf("HTTPAddr = %q, want :8090", cfg.HTTPAddr)
	}
	if cfg.Issuer != DefaultRouterIssuer {
		t.Errorf("Issuer = %q, want %q", cfg.Issuer, DefaultRouterIssuer)
	}

	// Missing signing key + better-auth inputs → invalid.
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for empty config")
	}

	// A fully-specified router config validates.
	seed := make([]byte, ed25519.SeedSize)
	cfg.Ed25519PrivateKey = base64.RawURLEncoding.EncodeToString(seed)
	cfg.MySQLDSN = "user:pw@tcp(db:3306)/gw"
	cfg.BetterAuthURL = "https://auth.example.com"
	cfg.BetterAuthJWKSURL = "https://auth.example.com/api/auth/jwks"
	cfg.PublicURL = "https://router.example.com"
	cfg.OIDCIssuer = cfg.PublicURL
	cfg.OIDCKeyEncKey = base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	cfg.OAuthClientSecretPepper = "test-pepper"
	cfg.RedisURL = "redis://localhost:6379"
	cfg.WebLoginURL = "https://web.example.com/login/whatsapp"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate on complete config: %v", err)
	}
}

// TestLoadRouter_JWKSDerivedAndPubSubDefault verifies dependent URLs derive from canonical base settings.
// It isolates environment inputs and compares the loaded values or validation error with the deployment contract.
// This catches configuration drift that could weaken trust assumptions or make startup behavior unpredictable.
func TestLoadRouter_JWKSDerivedAndPubSubDefault(t *testing.T) {
	for _, k := range []string{"BETTER_AUTH_JWKS_URL", "PUBSUB_REDIS_URL"} {
		t.Setenv(k, "")
	}
	t.Setenv("BETTER_AUTH_URL", "https://auth.example.com/")
	t.Setenv("REDIS_URL", "redis://localhost:6379")

	cfg, err := LoadRouter()
	if err != nil {
		t.Fatalf("LoadRouter: %v", err)
	}
	if cfg.BetterAuthJWKSURL != "https://auth.example.com/api/auth/jwks" {
		t.Errorf("derived JWKS = %q", cfg.BetterAuthJWKSURL)
	}
	if cfg.PubSubRedisURL != "redis://localhost:6379" {
		t.Errorf("PubSubRedisURL default = %q", cfg.PubSubRedisURL)
	}
}
