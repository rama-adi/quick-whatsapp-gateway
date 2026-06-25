package queue

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/hibiken/asynq"
)

// ParseRedisURL turns a REDIS_URL string (config §12) into an asynq.RedisClientOpt.
//
// We roll our own instead of asynq.ParseRedisURI because the latter ignores the
// username component (Redis 6+ ACL auth) and we want a single, well-tested entry
// point that also honours the db index from either the path (redis://h/2) or a
// ?db= query param. Supported schemes: redis:// and rediss:// (TLS).
//
// Supported forms:
//
//	redis://host:port
//	redis://host:port/2                 (db index in path)
//	redis://:password@host:port/0       (password only)
//	redis://user:password@host:port/0   (ACL username + password)
//	rediss://host:port                  (TLS)
//	redis://host:port?db=3              (db index as query param)
func ParseRedisURL(raw string) (asynq.RedisClientOpt, error) {
	var opt asynq.RedisClientOpt

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return opt, fmt.Errorf("parse REDIS_URL: empty url")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return opt, fmt.Errorf("parse REDIS_URL: %w", err)
	}

	switch u.Scheme {
	case "redis", "rediss":
		// ok
	default:
		return opt, fmt.Errorf("parse REDIS_URL: unsupported scheme %q (want redis:// or rediss://)", u.Scheme)
	}

	host := u.Host
	if host == "" {
		return opt, fmt.Errorf("parse REDIS_URL: missing host")
	}
	// Default the port to Redis's well-known 6379 when omitted, so "redis://localhost"
	// works without forcing every caller to spell out ":6379".
	if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
		host = net.JoinHostPort(host, "6379")
	}
	opt.Addr = host

	if u.User != nil {
		opt.Username = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			opt.Password = pw
		}
	}

	// DB index: prefer the path segment (redis://h/2), fall back to ?db=.
	db, err := parseDBIndex(u)
	if err != nil {
		return opt, err
	}
	opt.DB = db

	if u.Scheme == "rediss" {
		h, _, hostErr := net.SplitHostPort(opt.Addr)
		if hostErr != nil {
			h = opt.Addr
		}
		opt.TLSConfig = &tls.Config{ServerName: h}
	}

	return opt, nil
}

// parseDBIndex extracts the Redis db number from the URL path or ?db= query.
// Absent → 0. An invalid value is an error rather than a silent zero.
func parseDBIndex(u *url.URL) (int, error) {
	if p := strings.Trim(u.Path, "/"); p != "" {
		// Path may only carry the db number (redis has no nested paths here).
		if strings.Contains(p, "/") {
			return 0, fmt.Errorf("parse REDIS_URL: unexpected path %q", u.Path)
		}
		db, err := strconv.Atoi(p)
		if err != nil {
			return 0, fmt.Errorf("parse REDIS_URL: db index %q is not a number", p)
		}
		if db < 0 {
			return 0, fmt.Errorf("parse REDIS_URL: db index %d is negative", db)
		}
		return db, nil
	}
	if q := u.Query().Get("db"); q != "" {
		db, err := strconv.Atoi(q)
		if err != nil {
			return 0, fmt.Errorf("parse REDIS_URL: db query %q is not a number", q)
		}
		if db < 0 {
			return 0, fmt.Errorf("parse REDIS_URL: db index %d is negative", db)
		}
		return db, nil
	}
	return 0, nil
}
