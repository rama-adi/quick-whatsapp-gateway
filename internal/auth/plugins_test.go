package auth

import (
	"testing"

	eptypes "github.com/Authula/authula/plugins/email-password/types"
	rltypes "github.com/Authula/authula/plugins/rate-limit/types"
	secondarystorage "github.com/Authula/authula/plugins/secondary-storage"
)

// TestEmailPasswordSignUpGating verifies USER_PANEL_ENABLED drives DisableSignUp.
func TestEmailPasswordSignUpGating(t *testing.T) {
	tests := []struct {
		name             string
		userPanelEnabled bool
		wantDisable      bool
	}{
		{"user panel on -> sign-up open", true, false},
		{"user panel off -> sign-up disabled", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := buildPluginPlan(Config{UserPanelEnabled: tt.userPanelEnabled})
			cfg := plan.emailPasswordConfig()
			if !cfg.Enabled {
				t.Fatal("email-password plugin must always be enabled")
			}
			if cfg.DisableSignUp != tt.wantDisable {
				t.Fatalf("DisableSignUp = %v, want %v", cfg.DisableSignUp, tt.wantDisable)
			}
		})
	}
}

// TestSignUpEnabledMirrorsConfig checks the single source of truth for the gate.
func TestSignUpEnabledMirrorsConfig(t *testing.T) {
	if !(Config{UserPanelEnabled: true}).signUpEnabled() {
		t.Fatal("expected signUpEnabled() true when UserPanelEnabled")
	}
	if (Config{UserPanelEnabled: false}).signUpEnabled() {
		t.Fatal("expected signUpEnabled() false when not UserPanelEnabled")
	}
}

// TestPluginPlanEnablesAllPlugins asserts the full §10 plugin set is enabled,
// each via its Enabled:true config, without ever calling authula.New.
func TestPluginPlanEnablesAllPlugins(t *testing.T) {
	plan := buildPluginPlan(Config{UserPanelEnabled: true, RedisURL: "redis://localhost:6379/0"})

	checks := []struct {
		name    string
		enabled bool
	}{
		{"secondary-storage", plan.secondaryStorageConfig().Enabled},
		{"rate-limit", plan.rateLimitConfig().Enabled},
		{"session", plan.sessionConfig().Enabled},
		{"csrf", plan.csrfConfig().Enabled},
		{"email-password", plan.emailPasswordConfig().Enabled},
		{"totp", plan.totpConfig().Enabled},
		{"access-control", plan.accessControlConfig().Enabled},
		{"admin", plan.adminConfig().Enabled},
	}
	for _, c := range checks {
		if !c.enabled {
			t.Errorf("plugin %q must be enabled", c.name)
		}
	}
}

// TestSecondaryStorageUsesRedis verifies the Redis backing for secondary storage.
func TestSecondaryStorageUsesRedis(t *testing.T) {
	const url = "redis://localhost:6379/2"
	cfg := buildPluginPlan(Config{RedisURL: url}).secondaryStorageConfig()
	if cfg.Provider != secondarystorage.SecondaryStorageProviderRedis {
		t.Fatalf("provider = %q, want redis", cfg.Provider)
	}
	if cfg.Redis == nil || cfg.Redis.URL != url {
		t.Fatalf("redis URL not wired: %+v", cfg.Redis)
	}
}

// TestRateLimitUsesRedisProvider documents the explicit Redis provider choice.
func TestRateLimitUsesRedisProvider(t *testing.T) {
	cfg := buildPluginPlan(Config{}).rateLimitConfig()
	if cfg.Provider != rltypes.RateLimitProviderRedis {
		t.Fatalf("rate-limit provider = %q, want redis", cfg.Provider)
	}
}

// TestEmailPasswordAutoSignIn confirms a fresh sign-up is auto-logged-in so the
// caller gets a session token (admin bootstrap convenience).
func TestEmailPasswordAutoSignIn(t *testing.T) {
	var cfg eptypes.EmailPasswordPluginConfig = buildPluginPlan(Config{UserPanelEnabled: true}).emailPasswordConfig()
	if !cfg.AutoSignIn {
		t.Fatal("expected AutoSignIn true")
	}
}

// TestAdminRouteMappings asserts the enforce hook + permission gate on /admin/*.
// Paths are UN-prefixed; Authula prepends BasePath itself.
func TestAdminRouteMappings(t *testing.T) {
	mappings := adminRouteMappings("/auth")
	if len(mappings) != 1 {
		t.Fatalf("want 1 mapping, got %d", len(mappings))
	}
	m := mappings[0]
	if len(m.Paths) != 1 || m.Paths[0] != "/admin/*" {
		t.Fatalf("paths = %v, want [/admin/*] (un-prefixed)", m.Paths)
	}
	if !contains(m.Plugins, "access_control.enforce") {
		t.Fatalf("plugins = %v, want access_control.enforce", m.Plugins)
	}
	if !contains(m.Permissions, PermAdminAccess) {
		t.Fatalf("permissions = %v, want %q", m.Permissions, PermAdminAccess)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
