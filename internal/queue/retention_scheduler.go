package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const (
	// retentionClaimTTL keeps a completed day's claim long enough to cover
	// staggered replicas and a delayed scheduler tick, while naturally expiring
	// old schedule markers from Redis.
	retentionClaimTTL = 48 * time.Hour
	retentionRetry    = 5 * time.Minute
)

// retentionEnqueuer is the narrow Client surface the scheduler needs. Keeping
// it here makes the cross-replica scheduling policy testable without a running
// asynq worker.
type retentionEnqueuer interface {
	EnqueueRetentionPrune(context.Context, int64, ...asynq.Option) (*asynq.TaskInfo, error)
}

// RetentionSchedulerConfig controls the daily retention enqueue loop. A
// non-positive RetentionDays explicitly disables it.
type RetentionSchedulerConfig struct {
	RetentionDays int
	RedisPrefix   string
	Log           *slog.Logger

	// Now and RetryInterval are test hooks. Zero values use UTC wall clock and
	// the production retry interval.
	Now           func() time.Time
	RetryInterval time.Duration
}

// RetentionScheduler creates one deterministic retention task per UTC day. The
// Redis SET NX marker is shared by every gateway replica, so replicas may all
// run this scheduler but only one may enqueue that day's task.
type RetentionScheduler struct {
	redis     redis.Cmdable
	enqueuer  retentionEnqueuer
	days      int
	prefix    string
	log       *slog.Logger
	now       func() time.Time
	retryWait time.Duration
}

// NewRetentionScheduler constructs a retention scheduler. Start is a no-op
// when RetentionDays is zero, preserving RETENTION_DAYS=0 as keep-forever.
func NewRetentionScheduler(redisClient redis.Cmdable, enqueuer retentionEnqueuer, cfg RetentionSchedulerConfig) *RetentionScheduler {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	retry := cfg.RetryInterval
	if retry <= 0 {
		retry = retentionRetry
	}
	return &RetentionScheduler{
		redis:     redisClient,
		enqueuer:  enqueuer,
		days:      cfg.RetentionDays,
		prefix:    strings.Trim(strings.TrimSpace(cfg.RedisPrefix), ":"),
		log:       log,
		now:       now,
		retryWait: retry,
	}
}

// Start begins the daily loop and returns an idempotent graceful-stop function.
// The first enqueue is attempted immediately; successful or duplicate attempts
// then sleep until the next UTC day. A transient Redis/asynq failure is retried
// sooner, without waiting for tomorrow.
func (s *RetentionScheduler) Start(parent context.Context) func() {
	if s.days <= 0 {
		s.log.Info("retention scheduling disabled", "retention_days", s.days)
		return func() {}
	}
	if s.redis == nil || s.enqueuer == nil {
		s.log.Error("retention scheduling disabled: dependency missing")
		return func() {}
	}

	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			now := s.now().UTC()
			err := s.enqueueDay(ctx, now)
			if err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("retention schedule enqueue failed", "err", err)
			}

			wait := untilNextUTCDay(now)
			if err != nil && s.retryWait < wait {
				wait = s.retryWait
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

// enqueueDay makes today's UTC cutoff deterministic: it retains up to one
// extra partial day rather than risking deletion of rows newer than the stated
// number of full retention days. The deterministic day/cutoff also makes the
// cross-replica task identity easy to audit.
func (s *RetentionScheduler) enqueueDay(ctx context.Context, now time.Time) error {
	dayStart := utcDayStart(now)
	cutoff := dayStart.AddDate(0, 0, -s.days).UnixMilli()
	key := s.claimKey(dayStart)
	token, err := retentionClaimToken()
	if err != nil {
		return fmt.Errorf("make retention schedule claim token: %w", err)
	}
	claimed, err := s.redis.SetNX(ctx, key, token, retentionClaimTTL).Result()
	if err != nil {
		return fmt.Errorf("claim retention schedule %s: %w", dayStart.Format(time.DateOnly), err)
	}
	if !claimed {
		return nil
	}

	_, err = s.enqueuer.EnqueueRetentionPrune(ctx, cutoff,
		asynq.TaskID("retention:"+key),
	)
	if err == nil || errors.Is(err, asynq.ErrDuplicateTask) || errors.Is(err, asynq.ErrTaskIDConflict) {
		s.log.Info("retention prune scheduled", "cutoff_ms", cutoff, "date", dayStart.Format(time.DateOnly))
		return nil
	}

	// The claim was acquired before enqueueing, so release only our own claim on
	// failure. This permits the retry loop (or another replica) to recover while
	// never deleting a subsequently acquired marker.
	if releaseErr := s.releaseClaim(ctx, key, token); releaseErr != nil && !errors.Is(releaseErr, context.Canceled) {
		s.log.Warn("release failed retention schedule claim", "err", releaseErr, "date", dayStart.Format(time.DateOnly))
	}
	return fmt.Errorf("enqueue retention prune: %w", err)
}

func (s *RetentionScheduler) claimKey(day time.Time) string {
	prefix := s.prefix
	if prefix == "" {
		prefix = "gw"
	}
	return prefix + ":retention:scheduled:" + day.UTC().Format("20060102")
}

func (s *RetentionScheduler) releaseClaim(ctx context.Context, key, token string) error {
	const compareAndDelete = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
	return s.redis.Eval(ctx, compareAndDelete, []string{key}, token).Err()
}

func utcDayStart(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func untilNextUTCDay(now time.Time) time.Duration {
	return utcDayStart(now).AddDate(0, 0, 1).Sub(now.UTC())
}

func retentionClaimToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
