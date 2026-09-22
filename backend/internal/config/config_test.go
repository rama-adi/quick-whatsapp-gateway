package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// clearEnv unsets every ENV key Load reads so each test starts from a clean
// slate regardless of the host environment (and the t.Setenv calls restore the
// originals after the test).
func clearEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"GATEWAY_HTTP_ADDR", "GATEWAY_ID",
		"GATEWAY_CONTROL_PLANE_ADDR", "GATEWAY_CREDENTIAL_DIR", "GATEWAY_BOOTSTRAP_CA_FILE", "GATEWAY_ENROLLMENT_TOKEN", "GATEWAY_CERTIFICATE_RENEW_BEFORE",
		"GATEWAY_ENGINE_GRPC_ADDR", "GATEWAY_ENGINE_GRPC_ADVERTISE_ADDR", "GATEWAY_JOURNAL_PATH",
		"WHATSMEOW_STORE_DSN",
		"WHATSAPP_DEVICE_NAME",
		"DEFAULT_RATE_PER_MIN", "DEFAULT_RATE_PER_HOUR", "DEFAULT_AUTO_READ",
		"IGNORE_STATUS", "IGNORE_GROUPS", "IGNORE_CHANNELS", "IGNORE_BROADCAST",
		"LOG_LEVEL",
	}
	for _, k := range keys {
		t.Setenv(k, "")
		// t.Setenv sets to "" which Load treats as unset for getString/getInt/
		// getBool (they check ok && v != ""), so this is the clean default state.
	}
}

// TestLoadGateway_Defaults verifies an empty environment produces the documented safe defaults.
// It isolates environment inputs and compares the loaded values or validation error with the deployment contract.
// This catches configuration drift that could weaken trust assumptions or make startup behavior unpredictable.
func TestLoadGateway_Defaults(t *testing.T) {
	clearEnv(t)

	cfg, err := LoadGateway()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := &GatewayConfig{
		HTTPAddr:               ":8080",
		GatewayID:              "gw-1",
		CertificateRenewBefore: 0,
		WhatsmeowStoreDSN:      "file:store.db?_foreign_keys=on",
		WhatsAppDeviceName:     "",
		DefaultRatePerMin:      20,
		DefaultRatePerHour:     200,
		DefaultAutoRead:        true,
		IgnoreStatus:           false,
		IgnoreGroups:           false,
		IgnoreChannels:         false,
		IgnoreBroadcast:        false,
		LogLevel:               "info",
	}

	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load() defaults mismatch:\n got  %+v\n want %+v", cfg, want)
	}
}

// TestGatewayControlPlaneConfigIsMandatoryAndPathsAreStrict pins the Increment
// 9 cutover: the control plane is mandatory (no non-control mode exists). A
// minimal config without the
// control-plane triple is rejected; every configured path is validated strictly.
func TestGatewayControlPlaneConfigIsMandatoryAndPathsAreStrict(t *testing.T) {
	base := GatewayConfig{HTTPAddr: ":8080", WhatsmeowStoreDSN: "file:test.db", GatewayID: "gw-1", LogLevel: "info"}
	if err := base.Validate(); err == nil {
		t.Fatal("control-plane-less config accepted; the control plane is mandatory")
	}
	for name, mutate := range map[string]func(*GatewayConfig){
		"missing control addr": func(c *GatewayConfig) {
			c.CredentialDir = "/credentials"
			c.BootstrapCAFile = "/ca.pem"
			c.CertificateRenewBefore = time.Hour
		},
		"missing directory": func(c *GatewayConfig) {
			c.ControlPlaneAddr = "api:8443"
			c.BootstrapCAFile = "/ca.pem"
			c.CertificateRenewBefore = time.Hour
		},
		"missing CA": func(c *GatewayConfig) {
			c.ControlPlaneAddr = "api:8443"
			c.CredentialDir = "/credentials"
			c.CertificateRenewBefore = time.Hour
		},
		"relative directory": func(c *GatewayConfig) {
			c.ControlPlaneAddr = "api:8443"
			c.CredentialDir = "credentials"
			c.BootstrapCAFile = "/ca.pem"
			c.CertificateRenewBefore = time.Hour
		},
		"unclean CA": func(c *GatewayConfig) {
			c.ControlPlaneAddr = "api:8443"
			c.CredentialDir = "/credentials"
			c.BootstrapCAFile = "/tmp/../ca.pem"
			c.CertificateRenewBefore = time.Hour
		},
		"root directory": func(c *GatewayConfig) {
			c.ControlPlaneAddr = "api:8443"
			c.CredentialDir = "/"
			c.BootstrapCAFile = "/ca.pem"
			c.CertificateRenewBefore = time.Hour
		},
		"non-canonical gateway id": func(c *GatewayConfig) {
			full(t, c)
			c.GatewayID = "gw/1"
		},
		"zero renewal window": func(c *GatewayConfig) {
			full(t, c)
			c.CertificateRenewBefore = 0
		},
		"missing engine listener": func(c *GatewayConfig) {
			full(t, c)
			c.EngineGRPCAddr = ""
		},
		"relative journal path": func(c *GatewayConfig) {
			full(t, c)
			c.JournalPath = "journal/events.db"
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if cfg.Validate() == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	valid := base
	full(t, &valid)
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
}

// full fills cfg with a complete valid control-plane configuration.
func full(t *testing.T, c *GatewayConfig) {
	t.Helper()
	c.ControlPlaneAddr = "api:8443"
	c.CredentialDir = "/credentials"
	c.BootstrapCAFile = "/ca.pem"
	c.CertificateRenewBefore = time.Hour
	c.EngineGRPCAddr = ":9443"
	c.EngineGRPCAdvertise = "gw.example:9443"
	c.JournalPath = "/data/journal/events.db"
	c.WhatsmeowStoreDSN = "file:/data/keystore/store.db?_pragma=foreign_keys(on)"
}

// TestGatewayControlPlaneEnvTypoFailsValidation pins that a misspelled env var
// cannot silently skip the mandatory control plane: validation fails closed.
func TestGatewayControlPlaneEnvTypoFailsValidation(t *testing.T) {
	clearEnv(t)
	t.Setenv("GATEWAY_CONTROL_PLANE_ADR", "api:8443") // misspelled target
	t.Setenv("GATEWAY_CREDENTIAL_DIR", "/credentials")
	t.Setenv("GATEWAY_BOOTSTRAP_CA_FILE", "/ca.pem")
	cfg, err := LoadGateway()
	if err != nil {
		t.Fatal(err)
	}
	if err = cfg.Validate(); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("typo silently bypassed the mandatory control plane: %v", err)
	}
}

func TestLoadGateway_EndpointEnvPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		primary  string
		wantAddr string
	}{
		{"set", ":7001", ":7001"},
		{"defaults", "", ":8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("GATEWAY_HTTP_ADDR", tt.primary)
			cfg, err := LoadGateway()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.HTTPAddr != tt.wantAddr {
				t.Fatalf("endpoint = %q, want %q", cfg.HTTPAddr, tt.wantAddr)
			}
		})
	}
}

// TestLoadGateway_EnvOverride verifies every supported environment override is parsed and retained.
func TestLoadGateway_EnvOverride(t *testing.T) {
	clearEnv(t)

	t.Setenv("GATEWAY_HTTP_ADDR", ":9090")
	t.Setenv("GATEWAY_ID", "gw-east-1")
	t.Setenv("GATEWAY_CERTIFICATE_RENEW_BEFORE", "2h")
	t.Setenv("WHATSMEOW_STORE_DSN", "file:store.db?_foreign_keys=on")
	t.Setenv("WHATSAPP_DEVICE_NAME", "Acme Support")
	t.Setenv("DEFAULT_RATE_PER_MIN", "50")
	t.Setenv("DEFAULT_RATE_PER_HOUR", "500")
	t.Setenv("DEFAULT_AUTO_READ", "false")
	t.Setenv("IGNORE_STATUS", "true")
	t.Setenv("IGNORE_GROUPS", "1")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := LoadGateway()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"HTTPAddr", cfg.HTTPAddr, ":9090"},
		{"GatewayID", cfg.GatewayID, "gw-east-1"},
		{"CertificateRenewBefore", cfg.CertificateRenewBefore, 2 * time.Hour},
		{"WhatsmeowStoreDSN", cfg.WhatsmeowStoreDSN, "file:store.db?_foreign_keys=on"},
		{"WhatsAppDeviceName", cfg.WhatsAppDeviceName, "Acme Support"},
		{"DefaultRatePerMin", cfg.DefaultRatePerMin, 50},
		{"DefaultRatePerHour", cfg.DefaultRatePerHour, 500},
		{"DefaultAutoRead", cfg.DefaultAutoRead, false},
		{"IgnoreStatus", cfg.IgnoreStatus, true},
		{"IgnoreGroups", cfg.IgnoreGroups, true},
		{"LogLevel", cfg.LogLevel, "debug"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestLoadGateway_InvalidIntAndBoolFallBackToDefault verifies malformed optional values cannot erase defaults.
func TestLoadGateway_InvalidIntAndBoolFallBackToDefault(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEFAULT_RATE_PER_MIN", "not-a-number")
	t.Setenv("DEFAULT_AUTO_READ", "definitely-not-a-bool")

	cfg, err := LoadGateway()
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

// TestValidate table-tests required settings and cross-field constraints.
func TestValidate(t *testing.T) {
	// base returns a minimally-valid control-plane config that Validate accepts.
	base := func() *GatewayConfig {
		return &GatewayConfig{
			HTTPAddr:               ":8080",
			GatewayID:              "gw-1",
			ControlPlaneAddr:       "api:8443",
			CredentialDir:          "/credentials",
			BootstrapCAFile:        "/ca.pem",
			CertificateRenewBefore: time.Hour,
			EngineGRPCAddr:         ":9443",
			EngineGRPCAdvertise:    "gw.example:9443",
			JournalPath:            "/data/journal/events.db",
			WhatsmeowStoreDSN:      "file:/data/keystore/store.db?_pragma=foreign_keys(on)",
			DefaultRatePerMin:      20,
			DefaultRatePerHour:     200,
			LogLevel:               "info",
		}
	}

	tests := []struct {
		name    string
		mutate  func(*GatewayConfig)
		wantErr bool
	}{
		{"valid full config", func(*GatewayConfig) {}, false},
		{"valid uppercase log level", func(c *GatewayConfig) { c.LogLevel = "DEBUG" }, false},
		{"empty HTTP addr", func(c *GatewayConfig) { c.HTTPAddr = "" }, true},
		{"empty gateway id", func(c *GatewayConfig) { c.GatewayID = "" }, true},
		{"empty store dsn", func(c *GatewayConfig) { c.WhatsmeowStoreDSN = "" }, true},
		{"empty control addr", func(c *GatewayConfig) { c.ControlPlaneAddr = "" }, true},
		{"empty credential dir", func(c *GatewayConfig) { c.CredentialDir = "" }, true},
		{"empty bootstrap CA", func(c *GatewayConfig) { c.BootstrapCAFile = "" }, true},
		{"zero renewal window", func(c *GatewayConfig) { c.CertificateRenewBefore = 0 }, true},
		{"negative rate per min", func(c *GatewayConfig) { c.DefaultRatePerMin = -1 }, true},
		{"negative rate per hour", func(c *GatewayConfig) { c.DefaultRatePerHour = -1 }, true},
		{"bad log level", func(c *GatewayConfig) { c.LogLevel = "verbose" }, true},
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
