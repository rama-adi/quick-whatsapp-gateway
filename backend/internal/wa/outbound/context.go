package outbound

import "context"

// The account-global Sender holds a single WAClient, but the live whatsmeow
// clients are per-session (owned by the wa.Manager). The Sender therefore carries
// the target session id on the request context so a session-routing WAClient can
// resolve the correct per-session client for each call. The context key is
// unexported; WithSessionID / SessionIDFromContext are the only access points.

type ctxKey int

const ctxKeySessionID ctxKey = iota

// WithSessionID returns a child context carrying the target session id for the
// send. The Sender sets it before every dispatch; a routing WAClient reads it.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, ctxKeySessionID, sessionID)
}

// SessionIDFromContext returns the target session id set by the Sender, or "" if
// none is present (e.g. a direct dispatch in a unit test with a fake client).
func SessionIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeySessionID).(string); ok {
		return v
	}
	return ""
}
