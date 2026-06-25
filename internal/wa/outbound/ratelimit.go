package outbound

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisRateLimiter is a per-session send limiter backed by Redis using two
// fixed windows (per-minute and per-hour), as called for in §8
// (rate_per_min / rate_per_hour). Each window is a counter key with a TTL equal
// to the window length; the first INCR in a fresh window sets the TTL, so the
// window resets automatically when the key expires.
//
// Why fixed-window over a token bucket: it is exact against the masterplan's two
// stated budgets (N per minute AND M per hour), needs no background refill, and
// is trivially atomic in one Lua round-trip. The Lua script checks BOTH windows
// before incrementing EITHER, so a request that would breach the hour budget
// does not waste a minute token (and vice-versa) — the two counters never drift
// apart.
type redisRateLimiter struct {
	rdb *redis.Client
	// keyPrefix namespaces the limiter keys (default "wa:rl").
	keyPrefix string
}

// RedisRateLimiterOption configures a redisRateLimiter.
type RedisRateLimiterOption func(*redisRateLimiter)

// WithKeyPrefix overrides the Redis key namespace.
func WithKeyPrefix(prefix string) RedisRateLimiterOption {
	return func(r *redisRateLimiter) { r.keyPrefix = prefix }
}

// NewRedisRateLimiter builds a RateLimiter over the given go-redis client.
func NewRedisRateLimiter(rdb *redis.Client, opts ...RedisRateLimiterOption) RateLimiter {
	r := &redisRateLimiter{rdb: rdb, keyPrefix: "wa:rl"}
	for _, o := range opts {
		o(r)
	}
	return r
}

// allowScript atomically admits one request iff neither window is over budget.
//
// KEYS[1] = per-minute counter key, KEYS[2] = per-hour counter key
// ARGV[1] = per-minute limit, ARGV[2] = per-hour limit
// ARGV[3] = minute window seconds, ARGV[4] = hour window seconds
//
// Returns: { allowed (1/0), retryAfterSeconds }.
// A limit of <= 0 means "unlimited" for that window and is skipped.
var allowScript = redis.NewScript(`
local minLimit  = tonumber(ARGV[1])
local hourLimit = tonumber(ARGV[2])
local minTTL    = tonumber(ARGV[3])
local hourTTL   = tonumber(ARGV[4])

local minCount  = tonumber(redis.call('GET', KEYS[1]) or '0')
local hourCount = tonumber(redis.call('GET', KEYS[2]) or '0')

if minLimit > 0 and minCount >= minLimit then
  local ttl = redis.call('PTTL', KEYS[1])
  if ttl < 0 then ttl = minTTL * 1000 end
  return {0, math.ceil(ttl / 1000)}
end
if hourLimit > 0 and hourCount >= hourLimit then
  local ttl = redis.call('PTTL', KEYS[2])
  if ttl < 0 then ttl = hourTTL * 1000 end
  return {0, math.ceil(ttl / 1000)}
end

-- Admit: bump both counters, setting the TTL on the first hit of each window.
local newMin = redis.call('INCR', KEYS[1])
if newMin == 1 then redis.call('EXPIRE', KEYS[1], minTTL) end
local newHour = redis.call('INCR', KEYS[2])
if newHour == 1 then redis.call('EXPIRE', KEYS[2], hourTTL) end

return {1, 0}
`)

const (
	minuteWindowSeconds = 60
	hourWindowSeconds   = 3600
)

// Allow consumes one token from both the per-minute and per-hour windows for the
// session. When either window is exhausted it returns ok=false plus a retryAfter
// hint (seconds until the breached window resets).
func (r *redisRateLimiter) Allow(ctx context.Context, sessionID string, perMin, perHour int) (bool, time.Duration, error) {
	minKey := fmt.Sprintf("%s:%s:min", r.keyPrefix, sessionID)
	hourKey := fmt.Sprintf("%s:%s:hour", r.keyPrefix, sessionID)

	res, err := allowScript.Run(ctx, r.rdb, []string{minKey, hourKey},
		perMin, perHour, minuteWindowSeconds, hourWindowSeconds).Result()
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit: run script: %w", err)
	}

	vals, ok := res.([]interface{})
	if !ok || len(vals) != 2 {
		return false, 0, fmt.Errorf("ratelimit: unexpected script result %v", res)
	}
	allowed, _ := vals[0].(int64)
	retryAfterSec, _ := vals[1].(int64)
	if allowed == 1 {
		return true, 0, nil
	}
	return false, time.Duration(retryAfterSec) * time.Second, nil
}
