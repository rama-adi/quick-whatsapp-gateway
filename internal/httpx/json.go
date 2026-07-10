// Package httpx defines the low-level HTTP contract shared by gateway middleware
// and handlers that do not write through huma. It owns bounded and strict JSON
// decoding, JSON response encoding, domain-error-to-status mapping, stable list
// envelopes, and collision-free request context keys.
//
// The package deliberately contains transport policy rather than business logic.
// Invalid caller input becomes domain validation errors; unexpected internal
// errors are masked before serialization; pagination cursors remain opaque; and
// absent context values return explicit zero values. Keeping those rules here
// prevents individual handlers from drifting into incompatible or unsafe wire
// behavior.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// WriteJSON commits status and the canonical JSON content type, then encodes v
// with a trailing newline. A nil value intentionally produces a status-only
// response. Because net/http status and headers are committed before encoding,
// a response-marshaling failure can only be logged; callers should pass values
// known to be JSON-encodable and must not attempt a second response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httpx: encode response", "error", err)
	}
}

// statusForCode maps a §11 error code to its HTTP status. Unknown codes (and the
// zero value) fall through to 500.
func statusForCode(code string) int {
	switch code {
	case domain.CodeNotFound:
		return http.StatusNotFound
	case domain.CodeUnauthorized:
		return http.StatusUnauthorized
	case domain.CodeForbidden:
		return http.StatusForbidden
	case domain.CodeValidationError:
		return http.StatusBadRequest
	case domain.CodeRateLimited:
		return http.StatusTooManyRequests
	case domain.CodeConflict:
		return http.StatusConflict
	case domain.CodeNotImplemented:
		return http.StatusNotImplemented
	case domain.CodeUnavailable:
		return http.StatusServiceUnavailable
	case domain.CodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// WriteError serializes err as the §11 envelope {"error":{code,message,details}}
// and selects status solely from the stable domain code. errors.As preserves a
// wrapped *domain.APIError, allowing services to attach internal context without
// losing a safe client error. Every other error is replaced by a generic internal
// 500 so SQL, network, and credential details cannot cross the HTTP boundary; the
// original error must be logged by the caller or surrounding middleware.
func WriteError(w http.ResponseWriter, err error) {
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) {
		apiErr = domain.ErrInternal("internal server error")
	}
	WriteJSON(w, statusForCode(apiErr.Code), domain.ErrorBody{Error: apiErr})
}
