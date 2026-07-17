package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

// TestLoadAPI_DefaultsAndValidate verifies a complete API environment loads and validates with defaults.
// It isolates environment inputs and compares the loaded values or validation error with the deployment contract.
// This catches configuration drift that could weaken trust assumptions or make startup behavior unpredictable.
func TestLoadAPI_DefaultsAndValidate(t *testing.T) {
	keys := []string{
		"API_HTTP_ADDR", "API_PUBLIC_URL", "API_ISSUER", "API_ED25519_PRIVATE_KEY",
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

	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("LoadAPI: %v", err)
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

	// A fully-specified API config validates.
	seed := make([]byte, ed25519.SeedSize)
	cfg.Ed25519PrivateKey = base64.RawURLEncoding.EncodeToString(seed)
	cfg.MySQLDSN = "user:pw@tcp(db:3306)/gw"
	cfg.BetterAuthURL = "https://auth.example.com"
	cfg.BetterAuthJWKSURL = "https://auth.example.com/api/auth/jwks"
	cfg.PublicURL = "https://api.example.com"
	cfg.OIDCIssuer = cfg.PublicURL
	cfg.OIDCKeyEncKey = base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	cfg.OAuthClientSecretPepper = "test-pepper"
	cfg.RedisURL = "redis://localhost:6379"
	cfg.WebLoginURL = "https://web.example.com/login/whatsapp"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate on complete config: %v", err)
	}
}

func TestLoadAPI_RenamedEnvPrecedence(t *testing.T) {
	keys := []string{
		"API_HTTP_ADDR", "ROUTER_HTTP_ADDR", "API_PUBLIC_URL", "ROUTER_PUBLIC_URL",
		"API_ISSUER", "ROUTER_ISSUER", "API_ED25519_PRIVATE_KEY", "ROUTER_ED25519_PRIVATE_KEY",
	}
	tests := []struct {
		name                string
		primary, alias      string
		wantAddr, wantURL   string
		wantIssuer, wantKey string
	}{
		{"primary wins", "primary", "alias", "primary-addr", "primary-url", "primary-issuer", "primary-key"},
		{"deprecated aliases", "", "alias", "alias-addr", "alias-url", "alias-issuer", "alias-key"},
		{"defaults", "", "", ":8090", "", DefaultRouterIssuer, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range keys {
				t.Setenv(key, "")
			}
			if tt.primary != "" {
				t.Setenv("API_HTTP_ADDR", "primary-addr")
				t.Setenv("API_PUBLIC_URL", "primary-url")
				t.Setenv("API_ISSUER", "primary-issuer")
				t.Setenv("API_ED25519_PRIVATE_KEY", "primary-key")
			}
			if tt.alias != "" {
				t.Setenv("ROUTER_HTTP_ADDR", "alias-addr")
				t.Setenv("ROUTER_PUBLIC_URL", "alias-url")
				t.Setenv("ROUTER_ISSUER", "alias-issuer")
				t.Setenv("ROUTER_ED25519_PRIVATE_KEY", "alias-key")
			}
			cfg, err := LoadAPI()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.HTTPAddr != tt.wantAddr || cfg.PublicURL != tt.wantURL || cfg.Issuer != tt.wantIssuer || cfg.Ed25519PrivateKey != tt.wantKey {
				t.Fatalf("renamed env = (%q, %q, %q, %q), want (%q, %q, %q, %q)", cfg.HTTPAddr, cfg.PublicURL, cfg.Issuer, cfg.Ed25519PrivateKey, tt.wantAddr, tt.wantURL, tt.wantIssuer, tt.wantKey)
			}
		})
	}
}

// TestLoadAPI_JWKSDerivedAndPubSubDefault verifies dependent URLs derive from canonical base settings.
// It isolates environment inputs and compares the loaded values or validation error with the deployment contract.
// This catches configuration drift that could weaken trust assumptions or make startup behavior unpredictable.
func TestLoadAPI_JWKSDerivedAndPubSubDefault(t *testing.T) {
	for _, k := range []string{"BETTER_AUTH_JWKS_URL", "PUBSUB_REDIS_URL"} {
		t.Setenv(k, "")
	}
	t.Setenv("BETTER_AUTH_URL", "https://auth.example.com/")
	t.Setenv("REDIS_URL", "redis://localhost:6379")

	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("LoadAPI: %v", err)
	}
	if cfg.BetterAuthJWKSURL != "https://auth.example.com/api/auth/jwks" {
		t.Errorf("derived JWKS = %q", cfg.BetterAuthJWKSURL)
	}
	if cfg.PubSubRedisURL != "redis://localhost:6379" {
		t.Errorf("PubSubRedisURL default = %q", cfg.PubSubRedisURL)
	}
}
