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

func TestDecodeJSONUnknownField(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a","extra":1}`))
	var s sample
	assertValidation(t, DecodeJSON(r, &s))
}

func TestDecodeJSONMalformed(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":`))
	var s sample
	assertValidation(t, DecodeJSON(r, &s))
}

func TestDecodeJSONEmpty(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(``))
	var s sample
	assertValidation(t, DecodeJSON(r, &s))
}

func TestDecodeJSONWrongType(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"n":"notanint"}`))
	var s sample
	assertValidation(t, DecodeJSON(r, &s))
}

func TestDecodeJSONTrailingData(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a"}{"name":"b"}`))
	var s sample
	assertValidation(t, DecodeJSON(r, &s))
}

func TestDecodeJSONTooLarge(t *testing.T) {
	big := `{"name":"` + strings.Repeat("x", 100) + `"}`
	r := httptest.NewRequest("POST", "/", strings.NewReader(big))
	var s sample
	assertValidation(t, DecodeJSONLimit(r, &s, 16))
}
