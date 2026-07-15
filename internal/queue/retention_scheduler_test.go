package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

type retentionEnqueueFake struct {
	mu      sync.Mutex
	cutoffs []int64
	err     error
}

func (f *retentionEnqueueFake) EnqueueRetentionPrune(_ context.Context, cutoff int64, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.cutoffs = append(f.cutoffs, cutoff)
	return &asynq.TaskInfo{ID: "retention-task"}, nil
}

func (f *retentionEnqueueFake) calls() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.cutoffs...)
}

func newRetentionRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// Multiple gateway replicas may start together, but their daily Redis claim
// must permit exactly one retention enqueue for that UTC day.
func TestRetentionScheduler_DeduplicatesAcrossReplicas(t *testing.T) {
	rdb := newRetentionRedis(t)
	q := &retentionEnqueueFake{}
	now := time.Date(2026, time.July, 15, 13, 45, 0, 0, time.UTC)
	cfg := RetentionSchedulerConfig{RetentionDays: 30, RedisPrefix: "stack", Now: func() time.Time { return now }}
	one := NewRetentionScheduler(rdb, q, cfg)
	two := NewRetentionScheduler(rdb, q, cfg)

	if err := one.enqueueDay(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := two.enqueueDay(context.Background(), now); err != nil {
		t.Fatal(err)
	}

	got := q.calls()
	if len(got) != 1 {
		t.Fatalf("enqueues = %d, want 1 (%v)", len(got), got)
	}
	want := time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got[0] != want {
		t.Fatalf("cutoff = %d, want %d", got[0], want)
	}
	if exists, err := rdb.Exists(context.Background(), "stack:retention:scheduled:20260715").Result(); err != nil || exists != 1 {
		t.Fatalf("daily claim exists = %d, %v; want 1, nil", exists, err)
	}
}

// A failed enqueue must not burn the day-long cross-replica marker: a later
// retry can claim the day and enqueue the task.
func TestRetentionScheduler_ReleasesClaimAfterEnqueueFailure(t *testing.T) {
	rdb := newRetentionRedis(t)
	q := &retentionEnqueueFake{err: errors.New("redis unavailable")}
	now := time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC)
	s := NewRetentionScheduler(rdb, q, RetentionSchedulerConfig{RetentionDays: 7, RedisPrefix: "gw", Now: func() time.Time { return now }})

	if err := s.enqueueDay(context.Background(), now); err == nil {
		t.Fatal("enqueueDay succeeded, want error")
	}
	if exists, err := rdb.Exists(context.Background(), s.claimKey(now)).Result(); err != nil || exists != 0 {
		t.Fatalf("failed enqueue left claim: exists=%d err=%v", exists, err)
	}

	q.err = nil
	if err := s.enqueueDay(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if got := q.calls(); len(got) != 1 {
		t.Fatalf("successful retries = %d, want 1", len(got))
	}
}

// RETENTION_DAYS=0 must start no goroutine and enqueue nothing, while a
// positive setting attempts its first run immediately rather than after a day.
func TestRetentionScheduler_StartDisabledAndImmediate(t *testing.T) {
	rdb := newRetentionRedis(t)
	now := time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC)

	disabledQ := &retentionEnqueueFake{}
	disabled := NewRetentionScheduler(rdb, disabledQ, RetentionSchedulerConfig{RetentionDays: 0, Now: func() time.Time { return now }})
	disabled.Start(context.Background())()
	if got := disabledQ.calls(); len(got) != 0 {
		t.Fatalf("disabled enqueues = %d, want 0", len(got))
	}

	enabledQ := &retentionEnqueueFake{}
	enabled := NewRetentionScheduler(rdb, enabledQ, RetentionSchedulerConfig{
		RetentionDays: 1,
		RedisPrefix:   "immediate",
		Now:           func() time.Time { return now },
		RetryInterval: time.Millisecond,
	})
	stop := enabled.Start(context.Background())
	defer stop()

	deadline := time.After(time.Second)
	for len(enabledQ.calls()) == 0 {
		select {
		case <-deadline:
			t.Fatal("first retention enqueue waited instead of running immediately")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestUntilNextUTCDay(t *testing.T) {
	now := time.Date(2026, time.July, 15, 23, 59, 30, 0, time.FixedZone("WITA", 8*60*60))
	if got, want := untilNextUTCDay(now), 8*time.Hour+30*time.Second; got != want {
		t.Fatalf("until next UTC day = %s, want %s", got, want)
	}
}
