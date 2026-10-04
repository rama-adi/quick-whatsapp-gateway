package outbound

import (
	"context"
	"time"
)

type dispatchDeadlineKey struct{}

type dispatchDeadline struct {
	expiresAt int64
	now       func() time.Time
}

// WithDispatchDeadline carries the immutable command expiry to the final
// WhatsApp transport boundary. The clock is evaluated for every invocation,
// including each child of an album after an earlier child was acknowledged.
func WithDispatchDeadline(ctx context.Context, expiresAt *int64, now func() time.Time) context.Context {
	if expiresAt == nil {
		return ctx
	}
	return context.WithValue(ctx, dispatchDeadlineKey{}, dispatchDeadline{expiresAt: *expiresAt, now: now})
}

func checkDispatchDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Value(dispatchDeadlineKey{}).(dispatchDeadline); ok &&
		deadline.now().UnixMilli() >= deadline.expiresAt {
		return context.DeadlineExceeded
	}
	return nil
}
