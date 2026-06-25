package inbound

import (
	"context"
	"strings"
)

// NoopCommandRegistry is the v1 command registry: it recognizes no commands.
// The admin command surface (amlogin / WhatsApp-as-login) is v2 — but the
// interceptor still ships now (§6/§7), so a prefixed admin message is dropped
// even though the registry does nothing with it.
type NoopCommandRegistry struct{}

// NewNoopCommandRegistry returns a registry that handles nothing.
func NewNoopCommandRegistry() *NoopCommandRegistry { return &NoopCommandRegistry{} }

// Handle always returns handled=false, err=nil — no command is recognized in v1.
func (NoopCommandRegistry) Handle(ctx context.Context, sessionID, body string) (bool, error) {
	return false, nil
}

// interceptResult captures the outcome of the command-interceptor stage.
type interceptResult struct {
	// drop is true when the event must be removed from the pipeline (a prefixed
	// admin command): not persisted, not emitted, not counted.
	drop bool
}

// runInterceptor applies the §7.2 command-interceptor rule. It only fires for an
// admin session on an inbound text-bearing message whose body starts with the
// configured prefix. When it matches, the body is handed to the registry and the
// event is dropped regardless of whether the registry recognized it (a prefixed
// admin message is never a normal message in v1).
//
// The check is intentionally narrow: outbound echoes (FromMe), receipts, poll
// votes and non-text events are never intercepted, so an admin can still use the
// number normally (§6: it does double duty as a regular API number).
func (p *Pipeline) runInterceptor(ctx context.Context, isAdminSession bool, nm *NormalizedMessage) (interceptResult, error) {
	if !isAdminSession || p.cmdPrefix == "" {
		return interceptResult{}, nil
	}
	// Only inbound, non-echo, message-kind events with a body can be commands.
	if nm.FromMe || nm.Kind != KindMessage {
		return interceptResult{}, nil
	}
	body := strings.TrimSpace(nm.Body)
	if body == "" || !strings.HasPrefix(body, p.cmdPrefix) {
		return interceptResult{}, nil
	}
	// Hand to the registry; drop regardless of the handled flag. We surface the
	// registry error (wrapped) so callers can log it, but the drop still stands.
	if _, err := p.commands.Handle(ctx, nm.SessionID, body); err != nil {
		return interceptResult{drop: true}, &commandError{err: err}
	}
	return interceptResult{drop: true}, nil
}

// commandError wraps a CommandRegistry failure so it can be unwrapped with %w
// while still signaling that the event was dropped.
type commandError struct{ err error }

func (e *commandError) Error() string { return "command registry: " + e.err.Error() }
func (e *commandError) Unwrap() error { return e.err }
