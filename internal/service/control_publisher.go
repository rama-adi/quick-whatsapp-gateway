package service

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

// RedisControlPublisher JSON-encodes committed invalidation/revocation events
// onto Redis pubsub. A nil publisher is a deliberate single-process/no-bus
// configuration and behaves as a no-op.
type RedisControlPublisher struct {
	rdb *redis.Client
}

// NewRedisControlPublisher wraps the control Redis client; it owns no connection
// lifecycle and callers close the shared client at shutdown.
func NewRedisControlPublisher(rdb *redis.Client) *RedisControlPublisher {
	return &RedisControlPublisher{rdb: rdb}
}

func (p *RedisControlPublisher) Publish(ctx context.Context, channel string, payload any) error {
	if p == nil || p.rdb == nil {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return p.rdb.Publish(ctx, channel, raw).Err()
}
