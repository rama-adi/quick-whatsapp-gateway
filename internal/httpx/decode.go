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

// DefaultMaxBodyBytes is the allocation and read bound for ordinary JSON request
// bodies. Media bytes use separate upload paths, so 1 MiB leaves ample room for
// metadata while preventing an unauthenticated or malformed JSON body from
// consuming unbounded memory.
const DefaultMaxBodyBytes int64 = 1 << 20

// DecodeJSON decodes one request body into dst under DefaultMaxBodyBytes. The
// destination must be a non-nil pointer, fields must be declared by its JSON
// shape, and the stream must contain exactly one top-level value followed by EOF.
// Caller mistakes in the destination map to internal_error; all malformed caller
// input maps to validation_error and can be passed directly to WriteError.
func DecodeJSON[T any](r *http.Request, dst *T) error {
	return DecodeJSONLimit(r, dst, DefaultMaxBodyBytes)
}

// DecodeJSONLimit applies DecodeJSON's strictness with a caller-specific byte cap.
// It replaces r.Body with MaxBytesReader; the request owner remains responsible
// for closing r.Body, and closing the wrapper closes the underlying stream. A
// non-positive cap admits no non-empty body, which is useful only for explicitly
// bodyless contracts.
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

// decodeError classifies decoder and size-limit failures without exposing raw
// parser or reflection details. Syntax offsets and field names are safe enough to
// help callers repair input; invalid destinations are programming errors and are
// therefore masked as internal errors.
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
