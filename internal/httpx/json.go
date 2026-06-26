// Package httpx holds the shared HTTP transport primitives used by every API
// handler: JSON encoding, the §11 error envelope, request decoding with limits,
// cursor pagination, and the request-scoped context keys (organization, api key,
// request id). Handlers stay thin by delegating wire concerns here.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// WriteJSON encodes v as JSON with the given status code. A nil v writes only the
// status (no body). Encoding errors are logged but cannot change the already-sent
// status.
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
	case domain.CodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// WriteError serializes err as the §11 envelope {"error":{code,message,details}}
// and selects the HTTP status from the error code. A *domain.APIError is emitted
// verbatim; any other error is masked as a generic 500 "internal" error so
// internal details never leak to clients (the real error should be logged
// upstream, e.g. by the Recover/Logger middleware).
func WriteError(w http.ResponseWriter, err error) {
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) {
		apiErr = domain.ErrInternal("internal server error")
	}
	WriteJSON(w, statusForCode(apiErr.Code), domain.ErrorBody{Error: apiErr})
}
