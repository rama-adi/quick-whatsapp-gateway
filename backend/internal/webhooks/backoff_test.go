package webhooks

import "testing"

func TestBackoffSeconds_ClampsLargeAttempt(t *testing.T) {
	// A huge attempt index must not overflow; it clamps at maxBackoffShift.
	want := int64(2) * (int64(1) << maxBackoffShift)
	if got := backoffSeconds("exponential", 2, 1000); got != want {
		t.Errorf("clamp: got %d, want %d", got, want)
	}
}
