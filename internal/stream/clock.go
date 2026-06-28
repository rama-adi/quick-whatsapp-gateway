package stream

import "time"

// heartbeatInterval is the default cadence for {"event":"ping"} keep-alive frames
// (§9: every ~20s). Overridable via PumpConfig.Heartbeat for tests to shorten.
const heartbeatInterval = 20 * time.Second

// Ticker is the minimal slice of *time.Ticker the pump needs for heartbeats.
// Abstracting it lets tests drive the heartbeat deterministically with a fake
// ticker instead of waiting on wall-clock time.
type Ticker interface {
	// C returns the channel on which ticks are delivered.
	C() <-chan time.Time
	// Stop halts the ticker (no further ticks; channel is not closed).
	Stop()
}

// Clock creates Tickers. The production implementation wraps time.NewTicker.
type Clock interface {
	NewTicker(d time.Duration) Ticker
}

// realClock is the production Clock backed by the time package.
type realClock struct{}

// SystemClock is the default Clock used when none is injected.
var SystemClock Clock = realClock{}

func (realClock) NewTicker(d time.Duration) Ticker {
	return &realTicker{t: time.NewTicker(d)}
}

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time { return r.t.C }
func (r *realTicker) Stop()               { r.t.Stop() }
