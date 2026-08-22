package config

import (
	"encoding/base64"
	"testing"
	"time"
)

// TestLoadAPI_DefaultsAndValidate verifies a complete API environment loads and validates with defaults.
// It isolates environment inputs and compares the loaded values or validation error with the deployment contract.
// This catches configuration drift that could weaken trust assumptions or make startup behavior unpredictable.
func TestLoadAPI_DefaultsAndValidate(t *testing.T) {
	keys := []string{
		"API_HTTP_ADDR", "API_PUBLIC_GRPC_ADDR", "API_PUBLIC_URL",
		"ROUTER_HTTP_ADDR", "ROUTER_PUBLIC_URL",
		"BETTER_AUTH_URL", "BETTER_AUTH_JWKS_URL",
		"FRONTEND_ORIGINS", "MYSQL_DSN", "REDIS_URL", "PUBSUB_REDIS_URL",
		"REDIS_PREFIX", "OIDC_ISSUER", "OIDC_KEY_ENC_KEY", "OAUTH_CLIENT_SECRET_PEPPER",
		"WHATSAPP_ADMIN_CMD_PREFIX", "WEB_LOGIN_URL", "OIDC_REQUEST_TTL_SECONDS",
		"OIDC_AUTHCODE_TTL_SECONDS", "OIDC_TRUST_PROXY", "LOG_LEVEL", "API_GATEWAY_GRPC_ADDR",
		"API_GATEWAY_TLS_IDENTITY_DIR", "API_GATEWAY_TLS_RENEW_BEFORE", "PKI_ENCRYPTION_KEY", "PKI_ENCRYPTION_KEY_ID",
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
	if cfg.PublicGRPCAddr != ":8081" {
		t.Errorf("PublicGRPCAddr = %q, want :8081", cfg.PublicGRPCAddr)
	}
	if cfg.GatewayGRPCAddr != "" || cfg.GatewayTLSIdentityDir != "" || cfg.GatewayPKI != nil || cfg.GatewayTLSRenewBefore != 6*time.Hour {
		t.Fatalf("private gateway transport is not disabled by default: %+v", cfg)
	}

	// Missing better-auth inputs → invalid.
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for empty config")
	}

	// A fully-specified API config validates.
	cfg.MySQLDSN = "user:pw@tcp(db:3306)/gw"
	cfg.BetterAuthURL = "https://auth.example.com"
	cfg.BetterAuthJWKSURL = "https://auth.example.com/api/auth/jwks"
	cfg.PublicURL = "https://api.example.com"
	cfg.OIDCIssuer = cfg.PublicURL
	cfg.OIDCKeyEncKey = base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	cfg.OAuthClientSecretPepper = "test-pepper"
	cfg.AppEncryptionKey = base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	cfg.RedisURL = "redis://localhost:6379"
	cfg.WebLoginURL = "https://web.example.com/login/whatsapp"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate on complete config: %v", err)
	}
}

func TestLoadAPI_PublicGRPCAddrOverrideAndCollision(t *testing.T) {
	t.Setenv("API_HTTP_ADDR", ":9000")
	t.Setenv("API_PUBLIC_GRPC_ADDR", ":9001")
	cfg, err := LoadAPI()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicGRPCAddr != ":9001" {
		t.Fatalf("PublicGRPCAddr = %q, want :9001", cfg.PublicGRPCAddr)
	}
	cfg.HTTPAddr = ":9001"
	if err := cfg.Validate(); err == nil || err.Error() != "config: API_PUBLIC_GRPC_ADDR must differ from API_HTTP_ADDR" {
		t.Fatalf("collision error = %v", err)
	}
}

func TestAPIConfigListenAddressOverlap(t *testing.T) {
	tests := []struct {
		name, httpAddr, grpcAddr string
		wantOverlap              bool
	}{
		{"wildcard empty and IPv4", ":8090", "0.0.0.0:8090", true},
		{"localhost and IPv4 loopback", "localhost:8090", "127.0.0.1:8090", true},
		{"localhost and IPv6 loopback", "localhost:8090", "[::1]:8090", true},
		{"IPv4 mapped equivalent", "127.0.0.1:8090", "[::ffff:127.0.0.1]:8090", true},
		{"equivalent IPv6 literals", "[0:0:0:0:0:0:0:1]:8090", "[::1]:8090", true},
		{"distinct concrete hosts", "127.0.0.1:8090", "127.0.0.2:8090", false},
		{"distinct ports", ":8090", "0.0.0.0:8081", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tcpEndpointsOverlap(tt.httpAddr, tt.grpcAddr)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.wantOverlap {
				t.Fatalf("overlap = %v, want %v", got, tt.wantOverlap)
			}
		})
	}
}

func TestAPIConfigListenAddressRejectsHostnames(t *testing.T) {
	_, err := tcpEndpointsOverlap("api.internal:8090", ":8090")
	if err == nil || err.Error() != `API_HTTP_ADDR: host "api.internal" must be an IP literal, localhost, or wildcard` {
		t.Fatalf("hostname error = %v", err)
	}
}

func TestLoadAPI_RenamedEnvPrecedence(t *testing.T) {
	keys := []string{
		"API_HTTP_ADDR", "ROUTER_HTTP_ADDR", "API_PUBLIC_URL", "ROUTER_PUBLIC_URL",
	}
	tests := []struct {
		name              string
		primary, alias    string
		wantAddr, wantURL string
	}{
		{"primary wins", "primary", "alias", "primary-addr", "primary-url"},
		{"deprecated aliases", "", "alias", "alias-addr", "alias-url"},
		{"defaults", "", "", ":8090", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range keys {
				t.Setenv(key, "")
			}
			if tt.primary != "" {
				t.Setenv("API_HTTP_ADDR", "primary-addr")
				t.Setenv("API_PUBLIC_URL", "primary-url")
			}
			if tt.alias != "" {
				t.Setenv("ROUTER_HTTP_ADDR", "alias-addr")
				t.Setenv("ROUTER_PUBLIC_URL", "alias-url")
			}
			cfg, err := LoadAPI()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.HTTPAddr != tt.wantAddr || cfg.PublicURL != tt.wantURL {
				t.Fatalf("renamed env = (%q, %q), want (%q, %q)", cfg.HTTPAddr, cfg.PublicURL, tt.wantAddr, tt.wantURL)
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

func TestAPIPrivateGatewayConfig(t *testing.T) {
	t.Run("enabled load", func(t *testing.T) {
		t.Setenv("API_GATEWAY_GRPC_ADDR", ":8443")
		t.Setenv("API_GATEWAY_TLS_IDENTITY_DIR", "/var/lib/quick-wa/api-identity")
		t.Setenv("API_GATEWAY_TLS_RENEW_BEFORE", "4h")
		t.Setenv("API_GATEWAY_ENGINE_UNARY_DEADLINE", "2s")
		t.Setenv("PKI_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
		t.Setenv("PKI_ENCRYPTION_KEY_ID", "test-key")
		cfg, err := LoadAPI()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GatewayGRPCAddr != ":8443" || cfg.GatewayTLSIdentityDir != "/var/lib/quick-wa/api-identity" || cfg.GatewayTLSRenewBefore != 4*time.Hour || cfg.GatewayPKI == nil {
			t.Fatalf("private config=%+v", cfg)
		}
	})

	validPKI := &PKIConfig{EncryptionKey: make([]byte, 32), EncryptionKeyID: "k", LeafTTL: 24 * time.Hour, ClockSkew: time.Minute, RootTTL: 365 * 24 * time.Hour, IntermediateTTL: 90 * 24 * time.Hour, IntermediateRenewBefore: 30 * 24 * time.Hour}
	for name, cfg := range map[string]*APIConfig{
		"address only":   {HTTPAddr: ":8090", PublicGRPCAddr: ":8081", GatewayGRPCAddr: ":8443", GatewayTLSRenewBefore: time.Hour, GatewayPKI: validPKI},
		"directory only": {HTTPAddr: ":8090", PublicGRPCAddr: ":8081", GatewayTLSIdentityDir: "/tmp/id", GatewayTLSRenewBefore: time.Hour},
		"missing PKI":    {HTTPAddr: ":8090", PublicGRPCAddr: ":8081", GatewayGRPCAddr: ":8443", GatewayTLSIdentityDir: "/tmp/id", GatewayTLSRenewBefore: time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			if cfg.Validate() == nil {
				t.Fatal("partial private config accepted")
			}
		})
	}
	for name, privateAddr := range map[string]string{"HTTP overlap": "0.0.0.0:8090", "public gRPC overlap": "[::]:8081"} {
		t.Run(name, func(t *testing.T) {
			cfg := &APIConfig{HTTPAddr: ":8090", PublicGRPCAddr: ":8081", GatewayGRPCAddr: privateAddr, GatewayTLSIdentityDir: "/tmp/id", GatewayTLSRenewBefore: time.Hour, GatewayPKI: validPKI}
			if cfg.Validate() == nil {
				t.Fatal("private bind overlap accepted")
			}
		})
	}
	threshold := &APIConfig{HTTPAddr: ":8090", PublicGRPCAddr: ":8081", GatewayGRPCAddr: ":8443", GatewayTLSIdentityDir: "/tmp/id", GatewayTLSRenewBefore: validPKI.LeafTTL, GatewayPKI: validPKI}
	if err := threshold.Validate(); err == nil || err.Error() != "config: API_GATEWAY_TLS_RENEW_BEFORE must be shorter than PKI_LEAF_TTL" {
		t.Fatalf("renewal threshold error=%v", err)
	}
	for name, directory := range map[string]string{"relative": "identity", "dot dot": "/var/lib/../identity", "trailing separator": "/var/lib/identity/", "filesystem root": "/"} {
		t.Run(name, func(t *testing.T) {
			cfg := &APIConfig{HTTPAddr: ":8090", PublicGRPCAddr: ":8081", GatewayGRPCAddr: ":8443", GatewayTLSIdentityDir: directory, GatewayTLSRenewBefore: time.Hour, GatewayPKI: validPKI}
			if cfg.Validate() == nil {
				t.Fatalf("unsafe identity path %q accepted", directory)
			}
		})
	}
}
