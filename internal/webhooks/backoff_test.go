package webhooks

import (
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestBackoffSeconds_ExponentialSchedule(t *testing.T) {
	// delaySeconds=2, exponential: attempt 1..6 -> 2,4,8,16,32,64.
	want := []int64{2, 4, 8, 16, 32, 64}
	for i, w := range want {
		attempt := i + 1
		if got := backoffSeconds("exponential", 2, attempt); got != w {
			t.Errorf("attempt %d: got %d, want %d", attempt, got, w)
		}
	}
}

func TestBackoffSeconds_NonExponentialIsConstant(t *testing.T) {
	for attempt := 1; attempt <= 5; attempt++ {
		if got := backoffSeconds("linear", 5, attempt); got != 5 {
			t.Errorf("non-exponential attempt %d: got %d, want constant 5", attempt, got)
		}
	}
}

func TestBackoffSeconds_Defaults(t *testing.T) {
	// delaySeconds<=0 falls back to DefaultDelaySeconds (2); attempt<1 clamps to 1.
	if got := backoffSeconds("exponential", 0, 1); got != DefaultDelaySeconds {
		t.Errorf("zero delay: got %d, want %d", got, DefaultDelaySeconds)
	}
	if got := backoffSeconds("exponential", 2, 0); got != 2 {
		t.Errorf("attempt<1 should clamp to attempt 1 (=2): got %d", got)
	}
}

func TestBackoffSeconds_ClampsLargeAttempt(t *testing.T) {
	// A huge attempt index must not overflow; it clamps at maxBackoffShift.
	want := int64(2) * (int64(1) << maxBackoffShift)
	if got := backoffSeconds("exponential", 2, 1000); got != want {
		t.Errorf("clamp: got %d, want %d", got, want)
	}
}

func TestMaxAttempts(t *testing.T) {
	if got := maxAttempts(domain.RetryPolicy{Attempts: 15}); got != 15 {
		t.Errorf("got %d, want 15", got)
	}
	if got := maxAttempts(domain.RetryPolicy{Attempts: 0}); got != DefaultAttempts {
		t.Errorf("zero attempts: got %d, want %d", got, DefaultAttempts)
	}
}
