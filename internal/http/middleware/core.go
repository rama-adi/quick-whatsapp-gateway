package middleware

import (
	"log/slog"
	"net/http"
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
	if !s.written {
		s.status = code
		s.written = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.written {
		s.status = http.StatusOK
		s.written = true
	}
	return s.ResponseWriter.Write(b)
}

// Recover converts a panic in a downstream handler into a logged 500 JSON error
// (via httpx), so a single bad request can never crash the server. It must be the
// outermost middleware so it also catches panics in inner middleware.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
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

// RequestID ensures every request has a correlation id: it reuses an inbound
// X-Request-Id when present, otherwise mints a ULID. The id is stored in the
// context (httpx.RequestID) and echoed in the response header.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if id == "" {
				id = domain.NewULID()
			}
			w.Header().Set(RequestIDHeader, id)
			ctx := httpx.SetRequestID(r.Context(), id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Logger emits one structured slog line per request after it completes, with the
// method, path, status, duration, request id, and resolved tenant. Pair it after
// RequestID (and the auth middleware, so tenant is populated).
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
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
				"tenant", httpx.TenantID(r.Context()),
			)
		})
	}
}
