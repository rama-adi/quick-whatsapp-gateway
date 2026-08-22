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
	"time"

	"github.com/joho/godotenv"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
	sqlitestore "github.com/ramaadi/quick-whatsapp-gateway/internal/wa/store/sqlite"
)

// GatewayConfig is the fully-parsed gateway runtime configuration. Every field maps to an ENV
// var documented in masterplan §12. The gateway has no MySQL or Redis dependency:
// its stores are the gateway-local SQLite keystore and the event journal.
type GatewayConfig struct {
	// HTTP / server (operational probes only: healthz/readyz/metrics)
	HTTPAddr string // GATEWAY_HTTP_ADDR; deprecated fallback HTTP_ADDR

	// Gateway identity
	GatewayID              string        // GATEWAY_ID; canonical PKI identity name
	ControlPlaneAddr       string        // GATEWAY_CONTROL_PLANE_ADDR; required mTLS control stream
	CredentialDir          string        // GATEWAY_CREDENTIAL_DIR
	BootstrapCAFile        string        // GATEWAY_BOOTSTRAP_CA_FILE
	EnrollmentToken        string        // GATEWAY_ENROLLMENT_TOKEN; bootstrap-only, never persisted
	CertificateRenewBefore time.Duration // GATEWAY_CERTIFICATE_RENEW_BEFORE
	EngineGRPCAddr         string        // GATEWAY_ENGINE_GRPC_ADDR: private mTLS listener
	EngineGRPCAdvertise    string        // GATEWAY_ENGINE_GRPC_ADVERTISE_ADDR: canonical endpoint advertised to API
	JournalPath            string        // GATEWAY_JOURNAL_PATH: persistent gateway event journal

	// whatsmeow keystore — always SQLite in v2 (§6.1); the DSN points at the
	// gateway-local pure-Go SQLite file. No driver selection any more.
	WhatsmeowStoreDSN string // WHATSMEOW_STORE_DSN

	// WhatsApp pairing inputs. Session rows and admin bootstrap are API-owned;
	// these only label devices and seed defaults for assignments.
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
		GatewayID:              getString("GATEWAY_ID", "gw-1"),
		ControlPlaneAddr:       getString("GATEWAY_CONTROL_PLANE_ADDR", ""),
		CredentialDir:          getString("GATEWAY_CREDENTIAL_DIR", ""),
		BootstrapCAFile:        getString("GATEWAY_BOOTSTRAP_CA_FILE", ""),
		EnrollmentToken:        getString("GATEWAY_ENROLLMENT_TOKEN", ""),
		CertificateRenewBefore: getDuration("GATEWAY_CERTIFICATE_RENEW_BEFORE", 0),
		EngineGRPCAddr:         getString("GATEWAY_ENGINE_GRPC_ADDR", ""),
		EngineGRPCAdvertise:    getString("GATEWAY_ENGINE_GRPC_ADVERTISE_ADDR", ""),
		JournalPath:            getString("GATEWAY_JOURNAL_PATH", ""),
		WhatsmeowStoreDSN:      getString("WHATSMEOW_STORE_DSN", "file:store.db?_foreign_keys=on"),
		WhatsAppDeviceName:     getString("WHATSAPP_DEVICE_NAME", ""),
		DefaultRatePerMin:      getInt("DEFAULT_RATE_PER_MIN", 20),
		DefaultRatePerHour:     getInt("DEFAULT_RATE_PER_HOUR", 200),
		DefaultAutoRead:        getBool("DEFAULT_AUTO_READ", true),
		IgnoreStatus:           getBool("IGNORE_STATUS", false),
		IgnoreGroups:           getBool("IGNORE_GROUPS", false),
		IgnoreChannels:         getBool("IGNORE_CHANNELS", false),
		IgnoreBroadcast:        getBool("IGNORE_BROADCAST", false),
		LogLevel:               getString("LOG_LEVEL", "info"),
	}

	return cfg, nil
}

// Validate checks invariants that must hold before the server starts. The
// control plane is mandatory: a gateway without an mTLS control target cannot
// boot, because every operation and event flows through that stream.
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
	if c.ControlPlaneAddr == "" || c.CredentialDir == "" || c.BootstrapCAFile == "" {
		return fmt.Errorf("config: GATEWAY_CONTROL_PLANE_ADDR, GATEWAY_CREDENTIAL_DIR, and GATEWAY_BOOTSTRAP_CA_FILE are required — the gateway cannot run without its control plane")
	}
	if pki.ValidateGatewayID(c.GatewayID) != nil {
		return fmt.Errorf("config: GATEWAY_ID must be canonical for the private control plane")
	}
	if strings.TrimSpace(c.ControlPlaneAddr) != c.ControlPlaneAddr {
		return fmt.Errorf("config: GATEWAY_CONTROL_PLANE_ADDR must be canonical")
	}
	if c.CertificateRenewBefore <= 0 {
		return fmt.Errorf("config: GATEWAY_CERTIFICATE_RENEW_BEFORE must be positive")
	}
	if c.EngineGRPCAddr == "" || c.EngineGRPCAdvertise == "" || c.JournalPath == "" || !filepath.IsAbs(c.JournalPath) {
		return fmt.Errorf("config: GATEWAY_ENGINE_GRPC_ADDR, GATEWAY_ENGINE_GRPC_ADVERTISE_ADDR, and absolute GATEWAY_JOURNAL_PATH are required")
	}
	for name, value := range map[string]string{"GATEWAY_CREDENTIAL_DIR": c.CredentialDir, "GATEWAY_BOOTSTRAP_CA_FILE": c.BootstrapCAFile} {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) {
			return fmt.Errorf("config: %s must be an absolute clean non-root path", name)
		}
	}
	if _, err := sqlitestore.FilePath(c.WhatsmeowStoreDSN); err != nil {
		return fmt.Errorf("config: WHATSMEOW_STORE_DSN must be an explicit absolute persistent SQLite file path: %w", err)
	}

	if c.DefaultRatePerMin < 0 || c.DefaultRatePerHour < 0 {
		return fmt.Errorf("config: default rate limits must be non-negative")
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

func getDuration(key string, def time.Duration) time.Duration {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return def
	}
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return def
	}
	return duration
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
