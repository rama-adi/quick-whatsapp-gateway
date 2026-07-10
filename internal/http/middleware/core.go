package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// RequestIDHeader is the canonical header carrying the per-request correlation
// id, both inbound (honored if the client sends one) and outbound (always set).
const RequestIDHeader = "X-Request-Id"

// statusRecorder wraps http.ResponseWriter to capture the status code (and
// whether a body was written) for the Logger middleware. It defaults to 200,
// matching net/http's implicit WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.written {
		return
	}
	s.status = code
	s.written = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.written {
		s.status = http.StatusOK
		s.written = true
	}
	return s.ResponseWriter.Write(b)
}

// Flush forwards to the underlying ResponseWriter's Flusher so wrapping this
// recorder around a handler does not disable HTTP streaming. Embedding the
// http.ResponseWriter interface does NOT promote Flush (it lives on the
// separate http.Flusher interface), so the NDJSON stream handler's
// w.(http.Flusher) assertion would otherwise fail with "streaming unsupported".
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the wrapped writer to net/http's ResponseController, so any
// optional interface (Flusher, Hijacker, …) on the original writer stays
// reachable through this recorder.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Recover converts a panic in a downstream handler into a logged 500 JSON error
// (via httpx), so a single bad request can never crash the server. It must be the
// outermost middleware so it also catches panics in inner middleware.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	log = loggerOrDefault(log)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.ErrorContext(r.Context(), "panic recovered",
						"panic", rec,
						"method", r.Method,
						"path", r.URL.Path,
						"reqid", httpx.RequestID(r.Context()),
					)
					httpx.WriteError(w, domain.ErrInternal("internal server error"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestID ensures every request has a bounded correlation ID. A visible-ASCII
// inbound X-Request-Id of at most 128 bytes is preserved; empty, oversized, or
// control-character values are replaced with a ULID before entering logs or
// response headers. The accepted value is stored in context and echoed to the
// caller.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimSpace(r.Header.Get(RequestIDHeader))
			if !validRequestID(id) {
				id = domain.NewULID()
			}
			w.Header().Set(RequestIDHeader, id)
			ctx := httpx.SetRequestID(r.Context(), id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// validRequestID bounds untrusted correlation data before it reaches response
// headers and structured logs. Visible ASCII covers common UUID/ULID/trace IDs.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

// Logger emits one structured slog line per request after it completes, with the
// method, path, status, duration, request id, and resolved organization. Pair it after
// RequestID (and the auth middleware, so organization is populated).
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
	log = loggerOrDefault(log)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.InfoContext(r.Context(), "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"dur", time.Since(start),
				"reqid", httpx.RequestID(r.Context()),
				"organization", httpx.OrganizationID(r.Context()),
			)
		})
	}
}

func loggerOrDefault(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.Default()
	}
	return log
}
