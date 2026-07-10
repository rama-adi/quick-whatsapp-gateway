package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func decodeErr(t *testing.T, body io.Reader) domain.ErrorBody {
	t.Helper()
	var b domain.ErrorBody
	if err := json.NewDecoder(body).Decode(&b); err != nil {
		t.Fatalf("decode err body: %v", err)
	}
	return b
}

// --- fakes -------------------------------------------------------------------

type fakeLimiter struct {
	allow  bool
	err    error
	gotKey string
}

func (f *fakeLimiter) Allow(_ context.Context, key string) (bool, error) {
	f.gotKey = key
	return f.allow, f.err
}

// --- Recover -----------------------------------------------------------------

// TestRecover verifies the recover behavior remains part of the package contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestRecover(t *testing.T) {
	panicker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := Recover(discardLogger())(panicker)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if b := decodeErr(t, rec.Body); b.Error == nil || b.Error.Code != domain.CodeInternal {
		t.Fatalf("body = %+v", b.Error)
	}
}

// --- RequestID ---------------------------------------------------------------

// TestRequestIDGenerates verifies the request idgenerates behavior remains part of the package contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestRequestIDGenerates(t *testing.T) {
	var seen string
	h := RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestID(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if seen == "" {
		t.Fatal("expected request id in context")
	}
	if rec.Header().Get(RequestIDHeader) != seen {
		t.Fatalf("response header %q != ctx %q", rec.Header().Get(RequestIDHeader), seen)
	}
}

// TestRequestIDPropagatesInbound verifies the request idpropagates inbound behavior remains part of the package contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestRequestIDPropagatesInbound(t *testing.T) {
	var seen string
	h := RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestID(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(RequestIDHeader, "client-supplied-id")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seen != "client-supplied-id" {
		t.Fatalf("ctx id = %q, want client-supplied-id", seen)
	}
	if rec.Header().Get(RequestIDHeader) != "client-supplied-id" {
		t.Fatalf("echoed header = %q", rec.Header().Get(RequestIDHeader))
	}
}

// TestRequestIDRejectsUnboundedInboundValue verifies invalid or adversarial request idrejects unbounded inbound value input fails closed.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestRequestIDRejectsUnboundedInboundValue(t *testing.T) {
	var seen string
	h := RequestID()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestID(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, string(make([]byte, 129)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seen == "" || len(seen) > 128 {
		t.Fatalf("expected a bounded generated request id, got %q", seen)
	}
}

// --- Logger ------------------------------------------------------------------

// TestLoggerPassThrough verifies the logger pass through behavior remains part of the package contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestLoggerPassThrough(t *testing.T) {
	h := Logger(discardLogger())(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("logger altered response: %d %q", rec.Code, rec.Body.String())
	}
}

// TestMiddlewareNilLoggersDoNotPanic verifies optional logger dependencies cannot turn error handling into a panic.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestMiddlewareNilLoggersDoNotPanic(t *testing.T) {
	Logger(nil)(okHandler()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	Recover(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })).ServeHTTP(
		httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil),
	)
}

// TestStatusRecorderIgnoresDuplicateWriteHeader verifies the status recorder ignores duplicate write header behavior remains part of the package contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestStatusRecorderIgnoresDuplicateWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &statusRecorder{ResponseWriter: rec}
	s.WriteHeader(http.StatusCreated)
	s.WriteHeader(http.StatusInternalServerError)
	if rec.Code != http.StatusCreated || s.status != http.StatusCreated {
		t.Fatalf("status changed after duplicate WriteHeader: recorder=%d tracked=%d", rec.Code, s.status)
	}
}

// --- Timeout -----------------------------------------------------------------

// A handler that blocks on a downstream call (simulated by a select on ctx.Done)
// must have its request context cancelled by the deadline, so the handler unwinds
// and never hangs the caller forever.
// TestTimeoutCancelsWedgedHandler verifies the timeout cancels wedged handler behavior remains part of the package contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestTimeoutCancelsWedgedHandler(t *testing.T) {
	var ctxErr error
	blocked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a wedged ctx-aware DB query: it only returns when the request
		// context is cancelled (which is exactly how database/sql aborts a query).
		<-r.Context().Done()
		ctxErr = r.Context().Err()
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	h := Timeout(20 * time.Millisecond)(blocked)

	done := make(chan struct{})
	rec := httptest.NewRecorder()
	go func() {
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/x", nil))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler hung past the deadline; Timeout did not cancel the request context")
	}
	if !errors.Is(ctxErr, context.DeadlineExceeded) {
		t.Fatalf("ctx err = %v, want context.DeadlineExceeded", ctxErr)
	}
}

// A fast handler must not be affected by the deadline: it completes normally and
// the deadline is cancelled by the deferred cancel.
// TestTimeoutPassesFastHandler verifies adapter routing forwards the required timeout passes fast handler inputs without loss.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestTimeoutPassesFastHandler(t *testing.T) {
	h := Timeout(5 * time.Second)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("timeout altered fast response: %d %q", rec.Code, rec.Body.String())
	}
}

// d <= 0 is a passthrough (no deadline attached).
// TestTimeoutZeroIsPassthrough verifies the timeout zero is passthrough behavior remains part of the package contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestTimeoutZeroIsPassthrough(t *testing.T) {
	var hadDeadline bool
	h := Timeout(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadDeadline = r.Context().Deadline()
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if hadDeadline {
		t.Fatal("Timeout(0) must not attach a deadline")
	}
}

// --- RateLimit ---------------------------------------------------------------

// TestRateLimitAllow verifies the rate limit allow behavior remains part of the package contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestRateLimitAllow(t *testing.T) {
	lim := &fakeLimiter{allow: true}
	h := RateLimit(lim, nil)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestRateLimitDeny verifies rate-limit denial preserves the public 429 response contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestRateLimitDeny(t *testing.T) {
	lim := &fakeLimiter{allow: false}
	h := RateLimit(lim, nil)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
}

// TestRateLimitFailsOpenOnError verifies the rate limit fails open on error behavior remains part of the package contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestRateLimitFailsOpenOnError(t *testing.T) {
	lim := &fakeLimiter{allow: false, err: errors.New("redis down")}
	h := RateLimit(lim, nil)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open)", rec.Code)
	}
}

// TestRateLimitKeyBySession verifies the rate limit key by session behavior remains part of the package contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestRateLimitKeyBySession(t *testing.T) {
	lim := &fakeLimiter{allow: true}
	r := chi.NewRouter()
	r.With(RateLimit(lim, nil)).Get("/sessions/{session}/send", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/sessions/ses_42/send", nil))
	if lim.gotKey != "session:ses_42" {
		t.Fatalf("key = %q, want session:ses_42", lim.gotKey)
	}
}

// TestRateLimitKeyByOrganization verifies the rate limit key by organization behavior remains part of the package contract.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestRateLimitKeyByOrganization(t *testing.T) {
	lim := &fakeLimiter{allow: true}
	h := RateLimit(lim, nil)(okHandler())
	req := httptest.NewRequest("GET", "/webhooks", nil)
	req = req.WithContext(httpx.SetOrganizationID(req.Context(), "tnt_9"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if lim.gotKey != "organization:tnt_9" {
		t.Fatalf("key = %q, want organization:tnt_9", lim.gotKey)
	}
}

// The Logger wraps the ResponseWriter in statusRecorder to capture the status
// code. That wrapper MUST still expose http.Flusher, or the NDJSON event stream
// (which type-asserts w.(http.Flusher)) breaks with "streaming unsupported".
// TestLoggerPreservesFlusher verifies response instrumentation preserves streaming capabilities.
// It wraps a focused downstream handler and observes both response behavior and propagated request state.
// This protects transport middleware from corrupting cancellation, streaming, logging, or error semantics.
func TestLoggerPreservesFlusher(t *testing.T) {
	var sawFlusher bool
	h := Logger(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		sawFlusher = ok
		if ok {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("x"))
			f.Flush()
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/events", nil))
	if !sawFlusher {
		t.Fatal("Logger-wrapped ResponseWriter must implement http.Flusher (NDJSON stream needs it)")
	}
	if !rec.Flushed {
		t.Fatal("Flush did not forward to the underlying ResponseWriter")
	}
}
