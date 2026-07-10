// Package dbconn holds the shared MySQL/Redis connection helpers used by both
// entrypoints (the gateway and the router). Keeping one copy means the important
// DSN tweaks — parseTime so DATETIME round-trips, and clientFoundRows so an
// idempotent UPDATE reports a matched row rather than a misleading 0 — are applied
// identically wherever a process opens the shared stores.
package dbconn

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

// OpenMySQL opens and pings the shared app-data MySQL, applying the parseTime and
// clientFoundRows DSN params and the standard pool sizing. The ping is bounded
// so process startup cannot hang indefinitely; a failed ping closes the pool
// before returning, transferring no connection ownership to the caller.
func OpenMySQL(dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("MYSQL_DSN is required")
	}
	var err error
	dsn, err = normalizeMySQLDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return db, nil
}

// OpenRedis parses a REDIS_URL and returns a client (no ping; callers probe via
// readiness). A successful return transfers Close ownership to the caller;
// syntactically invalid configuration fails before allocating a client.
func OpenRedis(rawURL string) (*redis.Client, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("REDIS_URL is required")
	}
	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	return redis.NewClient(opt), nil
}

// normalizeMySQLDSN validates the DSN and enforces the two driver behaviours
// the repositories depend on. Parsing through the driver avoids brittle query
// string manipulation (notably parameter names appearing inside values).
// Explicit false values are intentionally overridden: timestamp scanning and
// matched-row accounting are repository invariants, not caller preferences.
func normalizeMySQLDSN(dsn string) (string, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("parse MYSQL_DSN: %w", err)
	}
	cfg.ParseTime = true
	cfg.ClientFoundRows = true
	return cfg.FormatDSN(), nil
}
