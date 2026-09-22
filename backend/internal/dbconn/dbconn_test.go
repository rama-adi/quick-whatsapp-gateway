package dbconn

import (
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestNormalizeMySQLDSN verifies required driver flags are forced without losing custom parameters.
// It starts with explicitly disabled flags and an unrelated parameter, then reparses the normalized DSN to check the effective driver contract.
func TestNormalizeMySQLDSN(t *testing.T) {
	t.Parallel()

	got, err := normalizeMySQLDSN("user:pass@tcp(localhost:3306)/app?application=web&parseTime=false&clientFoundRows=false")
	if err != nil {
		t.Fatalf("normalizeMySQLDSN: %v", err)
	}
	cfg, err := mysql.ParseDSN(got)
	if err != nil {
		t.Fatalf("parse normalized DSN: %v", err)
	}
	if !cfg.ParseTime || !cfg.ClientFoundRows {
		t.Fatalf("required flags not enabled: ParseTime=%v ClientFoundRows=%v", cfg.ParseTime, cfg.ClientFoundRows)
	}
	if cfg.Params["application"] != "web" {
		t.Fatalf("unrelated parameter changed: %#v", cfg.Params)
	}
}

// TestNormalizeMySQLDSNRejectsMalformedInput verifies configuration errors fail before dialing.
// The returned error must retain MYSQL_DSN context so startup diagnostics identify the bad setting.
func TestNormalizeMySQLDSNRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	_, err := normalizeMySQLDSN("not a valid dsn%zz")
	if err == nil || !strings.Contains(err.Error(), "parse MYSQL_DSN") {
		t.Fatalf("got error %v, want contextual parse error", err)
	}
}

// TestOpenConnectionRequiresConfiguration covers empty and malformed connection configuration.
// Both backends must reject missing settings immediately, and Redis parse failures must be wrapped with configuration context.
func TestOpenConnectionRequiresConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := OpenMySQL(""); err == nil {
		t.Fatal("OpenMySQL accepted an empty DSN")
	}
	if _, err := OpenRedis(""); err == nil {
		t.Fatal("OpenRedis accepted an empty URL")
	}
	if _, err := OpenRedis("://bad"); err == nil || !strings.Contains(err.Error(), "parse REDIS_URL") {
		t.Fatalf("OpenRedis malformed URL error = %v", err)
	}
}
