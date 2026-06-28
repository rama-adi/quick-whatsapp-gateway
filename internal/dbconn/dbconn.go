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
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // mysql driver
	"github.com/redis/go-redis/v9"
)

// OpenMySQL opens and pings the shared app-data MySQL, applying the parseTime and
// clientFoundRows DSN params and the standard pool sizing.
func OpenMySQL(dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("MYSQL_DSN is required")
	}
	dsn = ensureDSNParam(dsn, "parseTime", "parseTime=true")
	dsn = ensureDSNParam(dsn, "clientFoundRows", "clientFoundRows=true")
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// OpenRedis parses a REDIS_URL and returns a client (no ping; callers probe via
// readiness).
func OpenRedis(rawURL string) (*redis.Client, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("REDIS_URL is required")
	}
	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	return redis.NewClient(opt), nil
}

// ensureDSNParam appends `kv` (e.g. "parseTime=true") to a MySQL DSN's query
// string unless the named param is already present, picking the right `?`/`&`
// separator and leaving an explicitly-set value untouched.
func ensureDSNParam(dsn, name, kv string) string {
	if strings.Contains(dsn, name+"=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + kv
}
