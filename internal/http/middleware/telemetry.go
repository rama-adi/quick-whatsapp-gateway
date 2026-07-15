package middleware

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// SessionState is the safe, non-identifying subset of whatsmeow runtime state
// attached to failed session-scoped request events.
type SessionState struct {
	Status    string
	Connected bool
	LoggedIn  bool
}

// LoggerOptions supplies service-specific enrichers for the canonical request
// event. Nil providers simply omit their fields.
type LoggerOptions struct {
	Service      string
	DBStats      func() sql.DBStats
	SessionState func(sessionID string) (SessionState, bool)
}

type requestFailure struct {
	Cause        string
	Source       string
	ErrorType    string
	ContextError string
}

type requestTelemetry struct {
	mu           sync.Mutex
	failure      *requestFailure
	organization string
}

type requestTelemetryKey struct{}

var requestFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_request_failures_total",
	Help: "HTTP request failures classified by service hop, source, and cancellation cause.",
}, []string{"service", "source", "cause"})

func init() {
	prometheus.MustRegister(requestFailures)
}

func withRequestTelemetry(ctx context.Context) (context.Context, *requestTelemetry) {
	t := &requestTelemetry{}
	return context.WithValue(ctx, requestTelemetryKey{}, t), t
}

func telemetryFrom(ctx context.Context) *requestTelemetry {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(requestTelemetryKey{}).(*requestTelemetry)
	return t
}

// RecordFailure attaches the underlying failure to the request-wide event.
// Later calls replace earlier ones because the error closest to the response is
// the most useful explanation of the status sent to the caller.
func RecordFailure(ctx context.Context, source string, err error) {
	t := telemetryFrom(ctx)
	if t == nil || err == nil {
		return
	}
	f := classifyFailure(source, err)
	t.mu.Lock()
	t.failure = &f
	t.mu.Unlock()
}

// SetRequestOrganization enriches the request event after authentication. The
// mutable recorder is necessary because child request contexts do not propagate
// back out to the outer logging middleware.
func SetRequestOrganization(ctx context.Context, organization string) {
	t := telemetryFrom(ctx)
	if t == nil || organization == "" {
		return
	}
	t.mu.Lock()
	t.organization = organization
	t.mu.Unlock()
}

func classifyFailure(source string, err error) requestFailure {
	f := requestFailure{Cause: "error", Source: source, ErrorType: fmt.Sprintf("%T", err)}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		f.Cause = "deadline_exceeded"
		f.ContextError = context.DeadlineExceeded.Error()
	case errors.Is(err, context.Canceled):
		f.Cause = "context_canceled"
		f.ContextError = context.Canceled.Error()
	default:
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			f.Cause = "upstream_timeout"
			f.ContextError = ne.Error()
			break
		}
		var ae *domain.APIError
		if errors.As(err, &ae) {
			f.Cause = ae.Code
		}
	}
	return f
}

func (t *requestTelemetry) snapshot() (requestFailure, bool, string) {
	if t == nil {
		return requestFailure{}, false, ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failure == nil {
		return requestFailure{}, false, t.organization
	}
	return *t.failure, true, t.organization
}
