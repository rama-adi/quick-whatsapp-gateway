package auth

import (
	"fmt"
	"log/slog"
)

// Config holds everything internal/auth needs to wire Authula. Phase 3 (the
// composition root in cmd/server) fills this from ENV (§12) and passes it to
// Build. Keeping the surface a plain struct (no Authula types) lets the pure
// assembly logic be unit-tested without importing the heavy framework.
type Config struct {
	// AppName / BaseURL / BasePath shape the Authula HTTP surface. BasePath
	// defaults to "/auth" when empty (matches Authula's own default and §10).
	AppName  string
	BaseURL  string
	BasePath string

	// Secret is the Authula signing secret (APP secret / AUTHULA_SECRET, §10).
	// MUST be non-empty in production or Authula's NewConfig panics.
	Secret string

	// MySQLDSN is the app data DSN (go-sql-driver form, no scheme). Authula
	// opens its own pool against it via DatabaseConfig{Provider:"mysql"} unless
	// SharedDB is provided. See §4 / §10.
	MySQLDSN string

	// RedisURL backs Authula's secondary-storage plugin (§10). The plugin also
	// honors the REDIS_URL env var, which WINS over this value (recon §4).
	RedisURL string

	// UserPanelEnabled mirrors USER_PANEL_ENABLED (§12). When false, the
	// deployment is admin-only: self-registration via email-password sign-up is
	// disabled (DisableSignUp=true) so only the bootstrapped super-admin can log
	// in. Login itself always stays enabled.
	UserPanelEnabled bool

	// Logger is used for non-fatal wiring diagnostics. Optional; defaults to the
	// slog default logger.
	Logger *slog.Logger
}

// DefaultBasePath is Authula's (and our) mount prefix for all auth routes (§10).
const DefaultBasePath = "/auth"

// normalized returns a copy with defaults applied and required fields validated.
// Pure: no IO, no Authula construction — safe to unit-test.
func (c Config) normalized() (Config, error) {
	out := c
	if out.BasePath == "" {
		out.BasePath = DefaultBasePath
	}
	if out.Logger == nil {
		out.Logger = slog.Default()
	}
	if out.MySQLDSN == "" {
		return out, fmt.Errorf("auth: MySQLDSN is required")
	}
	return out, nil
}

// signUpEnabled reports whether email-password self-registration should be open.
// This is the single source of truth for the USER_PANEL_ENABLED gate and is what
// drives EmailPasswordPluginConfig.DisableSignUp (inverted). Pure & testable.
func (c Config) signUpEnabled() bool { return c.UserPanelEnabled }
