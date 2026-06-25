package outbound

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// newTestLimiter spins up an in-memory Redis and returns a limiter plus the
// miniredis handle (for fast-forwarding time) and a cleanup.
func newTestLimiter(t *testing.T) (RateLimiter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewRedisRateLimiter(rdb), mr
}

func TestRateLimiter_PerMinute(t *testing.T) {
	ctx := context.Background()
	rl, _ := newTestLimiter(t)

	const perMin, perHour = 3, 1000
	// First 3 are admitted.
	for i := 0; i < perMin; i++ {
		ok, _, err := rl.Allow(ctx, "sess1", perMin, perHour)
		require.NoError(t, err)
		require.Truef(t, ok, "request %d should be allowed", i)
	}
	// 4th breaches the minute window.
	ok, retryAfter, err := rl.Allow(ctx, "sess1", perMin, perHour)
	require.NoError(t, err)
	require.False(t, ok)
	require.Greater(t, retryAfter, time.Duration(0))
	require.LessOrEqual(t, retryAfter, time.Minute)
}

func TestRateLimiter_PerHour(t *testing.T) {
	ctx := context.Background()
	rl, _ := newTestLimiter(t)

	// Generous minute budget so only the hour window can bite.
	const perMin, perHour = 1000, 2
	for i := 0; i < perHour; i++ {
		ok, _, err := rl.Allow(ctx, "sess1", perMin, perHour)
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, retryAfter, err := rl.Allow(ctx, "sess1", perMin, perHour)
	require.NoError(t, err)
	require.False(t, ok)
	require.Greater(t, retryAfter, time.Minute) // hour window > 1 minute remaining
}

func TestRateLimiter_WindowReset(t *testing.T) {
	ctx := context.Background()
	rl, mr := newTestLimiter(t)

	const perMin, perHour = 1, 1000
	ok, _, err := rl.Allow(ctx, "sess1", perMin, perHour)
	require.NoError(t, err)
	require.True(t, ok)

	// Immediately blocked.
	ok, _, err = rl.Allow(ctx, "sess1", perMin, perHour)
	require.NoError(t, err)
	require.False(t, ok)

	// Advance past the minute window; the counter key expires and resets.
	mr.FastForward(61 * time.Second)

	ok, _, err = rl.Allow(ctx, "sess1", perMin, perHour)
	require.NoError(t, err)
	require.True(t, ok, "request should be allowed after window reset")
}

func TestRateLimiter_PerSessionIsolation(t *testing.T) {
	ctx := context.Background()
	rl, _ := newTestLimiter(t)

	const perMin, perHour = 1, 1000
	ok, _, err := rl.Allow(ctx, "sessA", perMin, perHour)
	require.NoError(t, err)
	require.True(t, ok)
	// sessA exhausted, but sessB has its own budget.
	ok, _, err = rl.Allow(ctx, "sessA", perMin, perHour)
	require.NoError(t, err)
	require.False(t, ok)
	ok, _, err = rl.Allow(ctx, "sessB", perMin, perHour)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestRateLimiter_UnlimitedWhenZero(t *testing.T) {
	ctx := context.Background()
	rl, _ := newTestLimiter(t)

	// Zero/negative limits mean "unlimited" for that window.
	for i := 0; i < 50; i++ {
		ok, _, err := rl.Allow(ctx, "sess1", 0, 0)
		require.NoError(t, err)
		require.True(t, ok)
	}
}
