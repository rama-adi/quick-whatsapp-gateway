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

// --- Logger ------------------------------------------------------------------

func TestLoggerPassThrough(t *testing.T) {
	h := Logger(discardLogger())(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("logger altered response: %d %q", rec.Code, rec.Body.String())
	}
}

// --- Timeout -----------------------------------------------------------------

// A handler that blocks on a downstream call (simulated by a select on ctx.Done)
// must have its request context cancelled by the deadline, so the handler unwinds
// and never hangs the caller forever.
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
func TestTimeoutPassesFastHandler(t *testing.T) {
	h := Timeout(5 * time.Second)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("timeout altered fast response: %d %q", rec.Code, rec.Body.String())
	}
}

// d <= 0 is a passthrough (no deadline attached).
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

func TestRateLimitAllow(t *testing.T) {
	lim := &fakeLimiter{allow: true}
	h := RateLimit(lim, nil)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRateLimitDeny(t *testing.T) {
	lim := &fakeLimiter{allow: false}
	h := RateLimit(lim, nil)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
}

func TestRateLimitFailsOpenOnError(t *testing.T) {
	lim := &fakeLimiter{allow: false, err: errors.New("redis down")}
	h := RateLimit(lim, nil)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open)", rec.Code)
	}
}

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
