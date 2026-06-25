package webhooks

import "github.com/ramaadi/quick-whatsapp-gateway/internal/domain"

// DefaultDelaySeconds and DefaultAttempts back-fill a RetryPolicy that arrives
// zero/under-specified (e.g. a webhook row written before retry tuning). They
// mirror the masterplan example {delaySeconds:2, attempts:15}.
const (
	DefaultDelaySeconds = 2
	DefaultAttempts     = 15

	// maxBackoffShift caps the exponential exponent so the shift can never
	// overflow int64 or schedule an absurd delay. 2^30 * delaySeconds is already
	// decades; beyond that we clamp.
	maxBackoffShift = 30
)

// backoffSeconds returns the delay before the given attempt number under an
// exponential policy with the supplied base delay.
//
// attempt is 1-based: attempt 1 is the delay BEFORE the first retry (i.e. after
// the initial send failed). The schedule is delay * 2^(attempt-1):
//
//	delaySeconds=2 -> 2, 4, 8, 16, 32, ...
//
// Non-exponential or unknown policies fall back to a constant delay (the base),
// which is the safest interpretation of an under-specified policy.
func backoffSeconds(policy string, delaySeconds, attempt int) int64 {
	if delaySeconds <= 0 {
		delaySeconds = DefaultDelaySeconds
	}
	if attempt < 1 {
		attempt = 1
	}
	if policy != "exponential" {
		return int64(delaySeconds)
	}
	shift := attempt - 1
	if shift > maxBackoffShift {
		shift = maxBackoffShift
	}
	return int64(delaySeconds) * (int64(1) << uint(shift))
}

// maxAttempts returns the configured attempt budget, applying the default when
// the policy under-specifies it. This is the total number of POST attempts; when
// attempts reaches this number with no success, the delivery is dead-lettered.
func maxAttempts(p domain.RetryPolicy) int {
	if p.Attempts <= 0 {
		return DefaultAttempts
	}
	return p.Attempts
}
