package service

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

type RedisControlPublisher struct {
	rdb *redis.Client
}

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
