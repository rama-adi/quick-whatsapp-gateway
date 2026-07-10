package config

import (
	"reflect"
	"testing"
)

// clearEnv unsets every ENV key Load reads so each test starts from a clean
// slate regardless of the host environment (and the t.Setenv calls restore the
// originals after the test).
func clearEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"HTTP_ADDR", "PUBLIC_URL", "GATEWAY_ID",
		"ROUTER_JWKS_URL", "ROUTER_ASSERTION_ISSUER",
		"BETTER_AUTH_URL", "BETTER_AUTH_JWKS_URL", "FRONTEND_ORIGINS",
		"APP_ENCRYPTION_KEY", "MYSQL_DSN",
		"WHATSMEOW_STORE_DSN", "REDIS_URL",
		"PUBSUB_REDIS_URL", "REDIS_PREFIX", "GATEWAY_ADMIN_USER_ID",
		"WHATSAPP_ADMIN_NUMBER",
		"WHATSAPP_ADMIN_CMD_PREFIX",
		"WHATSAPP_ADMIN_ORG_ID", "WHATSAPP_DEVICE_NAME",
		"DEFAULT_RATE_PER_MIN", "DEFAULT_RATE_PER_HOUR", "DEFAULT_AUTO_READ",
		"IGNORE_STATUS", "IGNORE_GROUPS", "IGNORE_CHANNELS", "IGNORE_BROADCAST",
		"WEBHOOK_URL", "WEBHOOK_EVENTS", "WEBHOOK_HMAC_KEY",
		"WEBHOOK_RETRIES_DELAY", "WEBHOOK_RETRIES_ATTEMPTS",
		"RETENTION_DAYS", "LOG_LEVEL",
	}
	for _, k := range keys {
		t.Setenv(k, "")
		// t.Setenv sets to "" which Load treats as unset for getString/getInt/
		// getBool (they check ok && v != ""), so this is the clean default state.
	}
}

// TestLoad_Defaults verifies an empty environment produces the documented safe defaults.
// It isolates environment inputs and compares the loaded values or validation error with the deployment contract.
// This catches configuration drift that could weaken trust assumptions or make startup behavior unpredictable.
func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := &Config{
		HTTPAddr:               ":8080",
		PublicURL:              "",
		GatewayID:              "gw-1",
		RouterJWKSURL:          "",
		RouterAssertionIssuer:  DefaultRouterIssuer,
		BetterAuthURL:          "",
		BetterAuthJWKSURL:      "",
		FrontendOrigins:        nil,
		AppEncryptionKey:       "",
		MySQLDSN:               "",
		WhatsmeowStoreDSN:      "file:store.db?_foreign_keys=on",
		RedisURL:               "",
		PubSubRedisURL:         "",
		RedisPrefix:            "gw",
		GatewayAdminUserID:     "",
		WhatsAppAdminNumber:    "",
		WhatsAppAdminCmdPrefix: "am",
		WhatsAppAdminOrgID:     "",
		WhatsAppDeviceName:     "",
		DefaultRatePerMin:      20,
		DefaultRatePerHour:     200,
		DefaultAutoRead:        true,
		IgnoreStatus:           false,
		IgnoreGroups:           false,
		IgnoreChannels:         false,
		IgnoreBroadcast:        false,
		WebhookURL:             "",
		WebhookEvents:          nil,
		WebhookHMACKey:         "",
		WebhookRetryDelay:      2,
		WebhookRetryAttempts:   15,
		RetentionDays:          0,
		LogLevel:               "info",
	}

	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load() defaults mismatch:\n got  %+v\n want %+v", cfg, want)
	}
}

// TestLoad_EnvOverride verifies every supported environment override is parsed and retained.
// It isolates environment inputs and compares the loaded values or validation error with the deployment contract.
// This catches configuration drift that could weaken trust assumptions or make startup behavior unpredictable.
func TestLoad_EnvOverride(t *testing.T) {
	clearEnv(t)

	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("PUBLIC_URL", "https://gw.example.com")
	t.Setenv("GATEWAY_ID", "gw-east-1")
	t.Setenv("BETTER_AUTH_URL", "https://auth.example.com")
	t.Setenv("FRONTEND_ORIGINS", "https://app.example.com, https://admin.example.com")
	t.Setenv("APP_ENCRYPTION_KEY", "deadbeef")
	t.Setenv("MYSQL_DSN", "user:pw@tcp(db:3306)/gw")
	t.Setenv("WHATSMEOW_STORE_DSN", "file:store.db?_foreign_keys=on")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("PUBSUB_REDIS_URL", "redis://control:6379")
	t.Setenv("REDIS_PREFIX", "stack-a")
	t.Setenv("GATEWAY_ADMIN_USER_ID", "user_admin_1")
	t.Setenv("WHATSAPP_DEVICE_NAME", "Acme Support")
	t.Setenv("DEFAULT_RATE_PER_MIN", "50")
	t.Setenv("DEFAULT_RATE_PER_HOUR", "500")
	t.Setenv("DEFAULT_AUTO_READ", "false")
	t.Setenv("IGNORE_STATUS", "true")
	t.Setenv("IGNORE_GROUPS", "1")
	t.Setenv("WEBHOOK_EVENTS", "message, poll.vote ,, group.update")
	t.Setenv("WEBHOOK_RETRIES_DELAY", "7")
	t.Setenv("WEBHOOK_RETRIES_ATTEMPTS", "3")
	t.Setenv("RETENTION_DAYS", "30")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"HTTPAddr", cfg.HTTPAddr, ":9090"},
		{"PublicURL", cfg.PublicURL, "https://gw.example.com"},
		{"GatewayID", cfg.GatewayID, "gw-east-1"},
		{"BetterAuthURL", cfg.BetterAuthURL, "https://auth.example.com"},
		// BETTER_AUTH_JWKS_URL unset → derived from BETTER_AUTH_URL.
		{"BetterAuthJWKSURL", cfg.BetterAuthJWKSURL, "https://auth.example.com/api/auth/jwks"},
		{"AppEncryptionKey", cfg.AppEncryptionKey, "deadbeef"},
		{"MySQLDSN", cfg.MySQLDSN, "user:pw@tcp(db:3306)/gw"},
		{"WhatsmeowStoreDSN", cfg.WhatsmeowStoreDSN, "file:store.db?_foreign_keys=on"},
		{"RedisURL", cfg.RedisURL, "redis://localhost:6379"},
		{"PubSubRedisURL", cfg.PubSubRedisURL, "redis://control:6379"},
		{"RedisPrefix", cfg.RedisPrefix, "stack-a"},
		{"GatewayAdminUserID", cfg.GatewayAdminUserID, "user_admin_1"},
		{"WhatsAppDeviceName", cfg.WhatsAppDeviceName, "Acme Support"},
		{"DefaultRatePerMin", cfg.DefaultRatePerMin, 50},
		{"DefaultRatePerHour", cfg.DefaultRatePerHour, 500},
		{"DefaultAutoRead", cfg.DefaultAutoRead, false},
		{"IgnoreStatus", cfg.IgnoreStatus, true},
		{"IgnoreGroups", cfg.IgnoreGroups, true},
		{"WebhookRetryDelay", cfg.WebhookRetryDelay, 7},
		{"WebhookRetryAttempts", cfg.WebhookRetryAttempts, 3},
		{"RetentionDays", cfg.RetentionDays, 30},
		{"LogLevel", cfg.LogLevel, "debug"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	// CSV parsing trims whitespace and drops empty fields.
	wantEvents := []string{"message", "poll.vote", "group.update"}
	if !reflect.DeepEqual(cfg.WebhookEvents, wantEvents) {
		t.Errorf("WebhookEvents = %v, want %v", cfg.WebhookEvents, wantEvents)
	}

	wantOrigins := []string{"https://app.example.com", "https://admin.example.com"}
	if !reflect.DeepEqual(cfg.FrontendOrigins, wantOrigins) {
		t.Errorf("FrontendOrigins = %v, want %v", cfg.FrontendOrigins, wantOrigins)
	}
}

// TestLoad_PubSubRedisURLDefaultsToRedisURL verifies pub/sub reuses the primary Redis URL when unset.
// It isolates environment inputs and compares the loaded values or validation error with the deployment contract.
// This catches configuration drift that could weaken trust assumptions or make startup behavior unpredictable.
func TestLoad_PubSubRedisURLDefaultsToRedisURL(t *testing.T) {
	clearEnv(t)
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	// PUBSUB_REDIS_URL deliberately left unset.

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PubSubRedisURL != "redis://localhost:6379" {
		t.Errorf("PubSubRedisURL = %q, want it to default to REDIS_URL", cfg.PubSubRedisURL)
	}
}

// TestLoad_InvalidIntAndBoolFallBackToDefault verifies malformed optional values cannot erase defaults.
// It isolates environment inputs and compares the loaded values or validation error with the deployment contract.
// This catches configuration drift that could weaken trust assumptions or make startup behavior unpredictable.
func TestLoad_InvalidIntAndBoolFallBackToDefault(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEFAULT_RATE_PER_MIN", "not-a-number")
	t.Setenv("DEFAULT_AUTO_READ", "definitely-not-a-bool")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DefaultRatePerMin != 20 {
		t.Errorf("invalid int should fall back to default 20, got %d", cfg.DefaultRatePerMin)
	}
	if cfg.DefaultAutoRead != true {
		t.Errorf("invalid bool should fall back to default true, got %v", cfg.DefaultAutoRead)
	}
}

// TestValidate table-tests required settings and cross-field production constraints.
// It isolates environment inputs and compares the loaded values or validation error with the deployment contract.
// This catches configuration drift that could weaken trust assumptions or make startup behavior unpredictable.
func TestValidate(t *testing.T) {
	// base returns a minimally-valid config that Validate accepts.
	base := func() *Config {
		return &Config{
			HTTPAddr:           ":8080",
			GatewayID:          "gw-1",
			WhatsmeowStoreDSN:  "file:store.db",
			DefaultRatePerMin:  20,
			DefaultRatePerHour: 200,
			RetentionDays:      0,
			LogLevel:           "info",
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid sqlite", func(*Config) {}, false},
		{"valid uppercase log level", func(c *Config) { c.LogLevel = "DEBUG" }, false},
		{"empty HTTP addr", func(c *Config) { c.HTTPAddr = "" }, true},
		{"empty gateway id", func(c *Config) { c.GatewayID = "" }, true},
		{"empty store dsn", func(c *Config) { c.WhatsmeowStoreDSN = "" }, true},
		{"negative rate per min", func(c *Config) { c.DefaultRatePerMin = -1 }, true},
		{"negative rate per hour", func(c *Config) { c.DefaultRatePerHour = -1 }, true},
		{"negative retention", func(c *Config) { c.RetentionDays = -1 }, true},
		{"bad log level", func(c *Config) { c.LogLevel = "verbose" }, true},
		{"admin number without org", func(c *Config) { c.WhatsAppAdminNumber = "628123456789" }, true},
		{"admin number with org", func(c *Config) {
			c.WhatsAppAdminNumber = "628123456789"
			c.WhatsAppAdminOrgID = "org_123"
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			tt.mutate(c)
			err := c.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}
