package wa

import (
	"math/rand"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types/events"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestClassifyEvent(t *testing.T) {
	tests := []struct {
		name          string
		evt           any
		wantStatus    domain.SessionStatus
		wantChanged   bool
		wantTerminal  bool
		wantKeepRecon bool
	}{
		{
			name:        "connected -> working, keep running",
			evt:         &events.Connected{},
			wantStatus:  domain.SessionWorking,
			wantChanged: true,
		},
		{
			name:        "pair success -> starting",
			evt:         &events.PairSuccess{},
			wantStatus:  domain.SessionStarting,
			wantChanged: true,
		},
		{
			name:         "logged out -> logged_out, stop reconnect",
			evt:          &events.LoggedOut{},
			wantStatus:   domain.SessionLoggedOut,
			wantChanged:  true,
			wantTerminal: true,
		},
		{
			name:         "stream replaced -> failed, stop reconnect",
			evt:          &events.StreamReplaced{},
			wantStatus:   domain.SessionFailed,
			wantChanged:  true,
			wantTerminal: true,
		},
		{
			name:         "temporary ban -> failed, stop reconnect",
			evt:          &events.TemporaryBan{},
			wantStatus:   domain.SessionFailed,
			wantChanged:  true,
			wantTerminal: true,
		},
		{
			name:         "client outdated -> failed, stop reconnect",
			evt:          &events.ClientOutdated{},
			wantStatus:   domain.SessionFailed,
			wantChanged:  true,
			wantTerminal: true,
		},
		{
			name:         "connect failure logged-out reason is fatal",
			evt:          &events.ConnectFailure{Reason: events.ConnectFailureLoggedOut},
			wantStatus:   domain.SessionFailed,
			wantChanged:  true,
			wantTerminal: true,
		},
		{
			name:         "connect failure banned reason is fatal",
			evt:          &events.ConnectFailure{Reason: events.ConnectFailureUnknownLogout},
			wantStatus:   domain.SessionFailed,
			wantChanged:  true,
			wantTerminal: true,
		},
		{
			name:        "connect failure generic is transient (keep retrying)",
			evt:         &events.ConnectFailure{Reason: events.ConnectFailureGeneric},
			wantChanged: false, // no status change; reconnect loop keeps trying
		},
		{
			name:        "disconnected is transient (no status change)",
			evt:         &events.Disconnected{},
			wantChanged: false,
		},
		{
			name:        "unrelated event ignored",
			evt:         &events.Receipt{},
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyEvent(tt.evt)
			if got.changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v", got.changed, tt.wantChanged)
			}
			if tt.wantChanged && got.status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", got.status, tt.wantStatus)
			}
			if got.terminal != tt.wantTerminal {
				t.Fatalf("terminal = %v, want %v", got.terminal, tt.wantTerminal)
			}
			if got.terminal && got.keepReconnect {
				t.Fatalf("terminal events must stop reconnect, keepReconnect = true")
			}
		})
	}
}

func TestIsFatalConnectFailure(t *testing.T) {
	fatal := []events.ConnectFailureReason{
		events.ConnectFailureLoggedOut,
		events.ConnectFailureTempBanned,
		events.ConnectFailureMainDeviceGone,
		events.ConnectFailureUnknownLogout,
		events.ConnectFailureClientOutdated,
		events.ConnectFailureBadUserAgent,
	}
	for _, r := range fatal {
		if !isFatalConnectFailure(r) {
			t.Errorf("reason %d should be fatal", r)
		}
	}
	if isFatalConnectFailure(events.ConnectFailureGeneric) {
		t.Errorf("generic (400) should be transient")
	}
}

func TestBackoffFor_Deterministic(t *testing.T) {
	cfg := backoffConfig{base: time.Second, max: 2 * time.Minute, factor: 2.0}

	// Same seed -> identical schedule.
	r1 := rand.New(rand.NewSource(42))
	r2 := rand.New(rand.NewSource(42))
	for attempt := 0; attempt < 10; attempt++ {
		d1 := backoffFor(cfg, attempt, r1)
		d2 := backoffFor(cfg, attempt, r2)
		if d1 != d2 {
			t.Fatalf("attempt %d: nondeterministic %v != %v", attempt, d1, d2)
		}
	}
}

func TestBackoffFor_FullJitterBounds(t *testing.T) {
	cfg := backoffConfig{base: time.Second, max: 8 * time.Second, factor: 2.0}
	rng := rand.New(rand.NewSource(7))

	// The uncapped ceiling per attempt is base*factor^n, clamped to max.
	ceilings := []time.Duration{
		1 * time.Second, // attempt 0: 1s
		2 * time.Second, // attempt 1: 2s
		4 * time.Second, // attempt 2: 4s
		8 * time.Second, // attempt 3: 8s
		8 * time.Second, // attempt 4: clamp at max
		8 * time.Second, // attempt 5: clamp at max
	}
	for attempt, ceil := range ceilings {
		// Sample several times; every sample must lie in [0, ceil].
		for i := 0; i < 200; i++ {
			d := backoffFor(cfg, attempt, rng)
			if d < 0 || d > ceil {
				t.Fatalf("attempt %d sample %d: %v out of [0,%v]", attempt, i, d, ceil)
			}
		}
	}
}

func TestBackoffFor_NeverExceedsMax(t *testing.T) {
	cfg := backoffConfig{base: time.Second, max: 30 * time.Second, factor: 3.0}
	rng := rand.New(rand.NewSource(99))
	for attempt := 0; attempt < 50; attempt++ {
		d := backoffFor(cfg, attempt, rng)
		if d > cfg.max {
			t.Fatalf("attempt %d: %v exceeds max %v", attempt, d, cfg.max)
		}
	}
}

func TestBackoffFor_NegativeAttemptTreatedAsZero(t *testing.T) {
	cfg := backoffConfig{base: time.Second, max: time.Minute, factor: 2.0}
	rng := rand.New(rand.NewSource(1))
	d := backoffFor(cfg, -5, rng)
	if d < 0 || d > cfg.base {
		t.Fatalf("negative attempt: %v not in [0,%v]", d, cfg.base)
	}
}
