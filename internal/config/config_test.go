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
		"HTTP_ADDR", "PUBLIC_URL", "APP_ENCRYPTION_KEY", "MYSQL_DSN",
		"WHATSMEOW_STORE_DRIVER", "WHATSMEOW_STORE_DSN", "REDIS_URL",
		"ADMIN_EMAIL", "ADMIN_PASSWORD", "WHATSAPP_ADMIN_NUMBER",
		"WHATSAPP_ADMIN_CMD_PREFIX", "USER_PANEL_ENABLED",
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

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := &Config{
		HTTPAddr:               ":8080",
		PublicURL:              "",
		AppEncryptionKey:       "",
		MySQLDSN:               "",
		WhatsmeowStoreDriver:   "sqlite",
		WhatsmeowStoreDSN:      "file:store.db?_foreign_keys=on",
		RedisURL:               "",
		AdminEmail:             "",
		AdminPassword:          "",
		WhatsAppAdminNumber:    "",
		WhatsAppAdminCmdPrefix: "am",
		UserPanelEnabled:       true,
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

func TestLoad_EnvOverride(t *testing.T) {
	clearEnv(t)

	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("PUBLIC_URL", "https://gw.example.com")
	t.Setenv("APP_ENCRYPTION_KEY", "deadbeef")
	t.Setenv("MYSQL_DSN", "user:pw@tcp(db:3306)/gw")
	t.Setenv("WHATSMEOW_STORE_DRIVER", "mysql")
	t.Setenv("WHATSMEOW_STORE_DSN", "user:pw@tcp(db:3306)/gw")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("USER_PANEL_ENABLED", "false")
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
		{"AppEncryptionKey", cfg.AppEncryptionKey, "deadbeef"},
		{"MySQLDSN", cfg.MySQLDSN, "user:pw@tcp(db:3306)/gw"},
		{"WhatsmeowStoreDriver", cfg.WhatsmeowStoreDriver, "mysql"},
		{"RedisURL", cfg.RedisURL, "redis://localhost:6379"},
		{"UserPanelEnabled", cfg.UserPanelEnabled, false},
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
}

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

func TestValidate(t *testing.T) {
	// base returns a minimally-valid config that Validate accepts.
	base := func() *Config {
		return &Config{
			HTTPAddr:             ":8080",
			WhatsmeowStoreDriver: "sqlite",
			WhatsmeowStoreDSN:    "file:store.db",
			DefaultRatePerMin:    20,
			DefaultRatePerHour:   200,
			RetentionDays:        0,
			LogLevel:             "info",
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid sqlite", func(*Config) {}, false},
		{"valid mysql", func(c *Config) {
			c.WhatsmeowStoreDriver = "mysql"
			c.MySQLDSN = "user:pw@tcp(db:3306)/gw"
			c.WhatsmeowStoreDSN = "user:pw@tcp(db:3306)/gw"
		}, false},
		{"valid uppercase log level", func(c *Config) { c.LogLevel = "DEBUG" }, false},
		{"empty HTTP addr", func(c *Config) { c.HTTPAddr = "" }, true},
		{"bad store driver", func(c *Config) { c.WhatsmeowStoreDriver = "postgres" }, true},
		{"empty store dsn", func(c *Config) { c.WhatsmeowStoreDSN = "" }, true},
		{"mysql driver without mysql dsn", func(c *Config) {
			c.WhatsmeowStoreDriver = "mysql"
			c.WhatsmeowStoreDSN = "user:pw@tcp(db:3306)/gw"
			c.MySQLDSN = ""
		}, true},
		{"negative rate per min", func(c *Config) { c.DefaultRatePerMin = -1 }, true},
		{"negative rate per hour", func(c *Config) { c.DefaultRatePerHour = -1 }, true},
		{"negative retention", func(c *Config) { c.RetentionDays = -1 }, true},
		{"bad log level", func(c *Config) { c.LogLevel = "verbose" }, true},
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
