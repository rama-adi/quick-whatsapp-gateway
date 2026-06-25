package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// DefaultMaxBodyBytes caps a decoded request body at 1 MiB. The send API never
// uploads media (metadata-only, masterplan §1), so this is generous for JSON.
const DefaultMaxBodyBytes int64 = 1 << 20

// DecodeJSON decodes the request body into dst. It enforces DefaultMaxBodyBytes,
// rejects unknown fields, and requires exactly one top-level JSON value. Any
// failure is returned as a *domain.APIError with code validation_error so the
// handler can pass it straight to WriteError.
func DecodeJSON[T any](r *http.Request, dst *T) error {
	return DecodeJSONLimit(r, dst, DefaultMaxBodyBytes)
}

// DecodeJSONLimit is DecodeJSON with a caller-chosen body cap (bytes).
func DecodeJSONLimit[T any](r *http.Request, dst *T, maxBytes int64) error {
	if r.Body == nil {
		return domain.ErrValidation("request body is required")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	// Reject trailing data (e.g. two JSON objects in one body).
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return domain.ErrValidation("request body must contain a single JSON object")
	}
	return nil
}

// decodeError translates json/http decode failures into friendly
// validation_error messages.
func decodeError(err error) error {
	var (
		syntaxErr     *json.SyntaxError
		typeErr       *json.UnmarshalTypeError
		maxBytesErr   *http.MaxBytesError
		invalidUnmErr *json.InvalidUnmarshalError
	)
	switch {
	case errors.Is(err, io.EOF):
		return domain.ErrValidation("request body must not be empty")
	case errors.As(err, &syntaxErr):
		return domain.ErrValidation(fmt.Sprintf("malformed JSON at byte %d", syntaxErr.Offset))
	case errors.As(err, &typeErr):
		return domain.ErrValidation(fmt.Sprintf("invalid value for field %q", typeErr.Field))
	case errors.As(err, &maxBytesErr):
		return domain.ErrValidation("request body too large")
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		return domain.ErrValidation("unknown field " + field)
	case errors.As(err, &invalidUnmErr):
		// Programming error (nil/non-pointer dst); surface generically.
		return domain.ErrInternal("decode target is invalid")
	default:
		return domain.ErrValidation("invalid request body")
	}
}
