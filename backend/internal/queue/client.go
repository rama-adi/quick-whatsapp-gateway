package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
)

// Client is a thin wrapper over asynq.Client that exposes typed enqueue helpers
// for the gateway's jobs. It owns the asynq connection and must be Closed on
// shutdown.
type Client struct {
	inner *asynq.Client
}

// NewClient constructs a Client from an asynq.RedisClientOpt (see ParseRedisURL).
// The connection is lazy — asynq dials on first enqueue.
func NewClient(redisOpt asynq.RedisClientOpt) *Client {
	return &Client{inner: asynq.NewClient(redisOpt)}
}

// Close releases the underlying Redis connection.
func (c *Client) Close() error {
	if c.inner == nil {
		return nil
	}
	return c.inner.Close()
}

// EnqueueRetentionPrune queues a one-off prune with the given epoch-ms cutoff.
// Routed to QueueRetention. The recurring daily prune (§5) registers this type
// with asynq's PeriodicTaskManager / Scheduler.
func (c *Client) EnqueueRetentionPrune(
	ctx context.Context,
	cutoffMs int64,
	opts ...asynq.Option,
) (*asynq.TaskInfo, error) {
	task, err := NewRetentionPruneTask(cutoffMs)
	if err != nil {
		return nil, err
	}
	info, err := c.inner.EnqueueContext(ctx, task, withDefaultQueue(QueueRetention, opts)...)
	if err != nil {
		return nil, fmt.Errorf("enqueue retention-prune: %w", err)
	}
	return info, nil
}

// withDefaultQueue prepends a default Queue option so the caller's opts win if
// they also specify a queue (asynq applies options left-to-right, last wins).
func withDefaultQueue(queue string, opts []asynq.Option) []asynq.Option {
	out := make([]asynq.Option, 0, len(opts)+1)
	out = append(out, asynq.Queue(queue))
	out = append(out, opts...)
	return out
}

// RetentionCutoffMs computes the epoch-ms cutoff for a retention window of
// retentionDays relative to now. retentionDays <= 0 means "keep forever" and
// returns ok=false so callers can skip enqueueing entirely (§5: 0 = keep).
func RetentionCutoffMs(now time.Time, retentionDays int) (cutoffMs int64, ok bool) {
	if retentionDays <= 0 {
		return 0, false
	}
	return now.Add(-time.Duration(retentionDays) * 24 * time.Hour).UnixMilli(), true
}
