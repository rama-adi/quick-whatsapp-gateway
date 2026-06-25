// Package config loads, defaults, and validates the gateway's runtime
// configuration from environment variables (see masterplan §12). A .env file is
// loaded on boot when present (handy for local dev) and ignored otherwise.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config is the fully-parsed runtime configuration. Every field maps to an ENV
// var documented in masterplan §12.
type Config struct {
	// HTTP / server
	HTTPAddr  string // HTTP_ADDR
	PublicURL string // PUBLIC_URL

	// Secrets
	AppEncryptionKey string // APP_ENCRYPTION_KEY (base64 32-byte AES-GCM key)

	// App data store
	MySQLDSN string // MYSQL_DSN

	// whatsmeow keystore
	WhatsmeowStoreDriver string // WHATSMEOW_STORE_DRIVER: mysql | sqlite
	WhatsmeowStoreDSN    string // WHATSMEOW_STORE_DSN

	// Infra
	RedisURL string // REDIS_URL

	// Admin bootstrap
	AdminEmail    string // ADMIN_EMAIL
	AdminPassword string // ADMIN_PASSWORD

	// Admin WhatsApp number
	WhatsAppAdminNumber    string // WHATSAPP_ADMIN_NUMBER
	WhatsAppAdminCmdPrefix string // WHATSAPP_ADMIN_CMD_PREFIX

	// Panels
	UserPanelEnabled bool // USER_PANEL_ENABLED

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

// Load reads configuration from the environment, applying defaults from
// masterplan §12. It loads a .env file first if one exists in the working
// directory (a no-op when absent, so production can inject real env vars).
func Load() (*Config, error) {
	// Best-effort .env load; ignore "not found" so prod is unaffected.
	_ = godotenv.Load()

	cfg := &Config{
		HTTPAddr:               getString("HTTP_ADDR", ":8080"),
		PublicURL:              getString("PUBLIC_URL", ""),
		AppEncryptionKey:       getString("APP_ENCRYPTION_KEY", ""),
		MySQLDSN:               getString("MYSQL_DSN", ""),
		WhatsmeowStoreDriver:   getString("WHATSMEOW_STORE_DRIVER", "sqlite"),
		WhatsmeowStoreDSN:      getString("WHATSMEOW_STORE_DSN", "file:store.db?_foreign_keys=on"),
		RedisURL:               getString("REDIS_URL", ""),
		AdminEmail:             getString("ADMIN_EMAIL", ""),
		AdminPassword:          getString("ADMIN_PASSWORD", ""),
		WhatsAppAdminNumber:    getString("WHATSAPP_ADMIN_NUMBER", ""),
		WhatsAppAdminCmdPrefix: getString("WHATSAPP_ADMIN_CMD_PREFIX", "am"),
		UserPanelEnabled:       getBool("USER_PANEL_ENABLED", true),
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

	return cfg, nil
}

// Validate checks invariants that must hold before the server starts. It is
// intentionally lenient about secrets that are only required by features filled
// in by later milestones; those subsystems validate their own prerequisites.
func (c *Config) Validate() error {
	if c.HTTPAddr == "" {
		return fmt.Errorf("config: HTTP_ADDR must not be empty")
	}

	switch c.WhatsmeowStoreDriver {
	case "mysql", "sqlite":
	default:
		return fmt.Errorf("config: WHATSMEOW_STORE_DRIVER must be 'mysql' or 'sqlite', got %q", c.WhatsmeowStoreDriver)
	}
	if c.WhatsmeowStoreDSN == "" {
		return fmt.Errorf("config: WHATSMEOW_STORE_DSN must not be empty")
	}
	if c.WhatsmeowStoreDriver == "mysql" && c.MySQLDSN == "" {
		return fmt.Errorf("config: MYSQL_DSN is required when WHATSMEOW_STORE_DRIVER=mysql")
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
