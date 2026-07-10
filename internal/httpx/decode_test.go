package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

type sample struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// TestDecodeJSONOK decodes one complete object into a typed destination under the default size cap.
// It expects every known field to retain its JSON value and no validation error.
// This is the success baseline for the stricter rejection tests below.
func TestDecodeJSONOK(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a","n":2}`))
	var s sample
	if err := DecodeJSON(r, &s); err != nil {
		t.Fatalf("DecodeJSON: %v", err)
	}
	if s.Name != "a" || s.N != 2 {
		t.Fatalf("decoded = %+v", s)
	}
}

func assertValidation(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*domain.APIError)
	if !ok || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("expected validation_error, got %v", err)
	}
}

// TestDecodeJSONUnknownField sends a well-formed object containing an undeclared property.
// It expects validation_error rather than silently discarding a caller typo.
// Strict fields prevent clients from believing unsupported or misspelled security options took effect.
func TestDecodeJSONUnknownField(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a","extra":1}`))
	var s sample
	assertValidation(t, DecodeJSON(r, &s))
}

// TestDecodeJSONMalformed supplies truncated JSON that cannot form one object.
// It expects a client-facing validation_error instead of a panic or internal error.
// This pins safe syntax-error mapping at the transport boundary.
func TestDecodeJSONMalformed(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":`))
	var s sample
	assertValidation(t, DecodeJSON(r, &s))
}

// TestDecodeJSONEmpty supplies a present but zero-length request body.
// It expects validation_error because mutation endpoints require one explicit object.
// Treating EOF as invalid avoids accidentally applying zero-value commands.
func TestDecodeJSONEmpty(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(``))
	var s sample
	assertValidation(t, DecodeJSON(r, &s))
}

// TestDecodeJSONWrongType places a string in a field declared as an integer.
// It expects validation_error with no partial command accepted by the caller.
// This preserves typed handler invariants before service logic runs.
func TestDecodeJSONWrongType(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"n":"notanint"}`))
	var s sample
	assertValidation(t, DecodeJSON(r, &s))
}

// TestDecodeJSONTrailingData concatenates two individually valid JSON objects in one body.
// It expects rejection because the decoder contract permits exactly one top-level value.
// This prevents request-smuggling ambiguity between middleware and handlers.
func TestDecodeJSONTrailingData(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a"}{"name":"b"}`))
	var s sample
	assertValidation(t, DecodeJSON(r, &s))
}

// TestDecodeJSONTooLarge sets a deliberately tiny limit and sends an otherwise valid larger object.
// It expects validation_error before unbounded data can be retained in the destination.
// This guards the per-request memory bound used against oversized JSON payloads.
func TestDecodeJSONTooLarge(t *testing.T) {
	big := `{"name":"` + strings.Repeat("x", 100) + `"}`
	r := httptest.NewRequest("POST", "/", strings.NewReader(big))
	var s sample
	assertValidation(t, DecodeJSONLimit(r, &s, 16))
}
