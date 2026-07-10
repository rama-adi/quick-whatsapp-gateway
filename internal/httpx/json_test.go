package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// TestWriteJSON writes a successful object response with a non-default status.
// It expects the requested status, canonical JSON content type, and decodable body.
// This pins the common response contract used by non-huma handlers and middleware.
func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusCreated, map[string]string{"hi": "there"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["hi"] != "there" {
		t.Fatalf("body = %v", got)
	}
}

// TestWriteErrorMapping table-tests every public domain error code against its HTTP status.
// Each case must retain the code and message inside the standard error envelope.
// This prevents refactors from changing client retry or authorization behavior through status drift.
func TestWriteErrorMapping(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{domain.CodeNotFound, http.StatusNotFound},
		{domain.CodeUnauthorized, http.StatusUnauthorized},
		{domain.CodeForbidden, http.StatusForbidden},
		{domain.CodeValidationError, http.StatusBadRequest},
		{domain.CodeRateLimited, http.StatusTooManyRequests},
		{domain.CodeConflict, http.StatusConflict},
		{domain.CodeNotImplemented, http.StatusNotImplemented},
		{domain.CodeInternal, http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteError(rec, domain.NewAPIError(c.code, "boom"))
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d", rec.Code, c.want)
			}
			var body domain.ErrorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if body.Error == nil || body.Error.Code != c.code || body.Error.Message != "boom" {
				t.Fatalf("envelope = %+v", body.Error)
			}
		})
	}
}

// TestWriteErrorMasksNonAPIError passes a raw infrastructure error containing sensitive text.
// It expects a generic internal 500 envelope and verifies the original message is absent.
// This is the regression guard against leaking database or credential details to callers.
func TestWriteErrorMasksNonAPIError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, errors.New("raw db connection string leak"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body domain.ErrorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error == nil || body.Error.Code != domain.CodeInternal {
		t.Fatalf("expected internal code, got %+v", body.Error)
	}
	if body.Error.Message == "raw db connection string leak" {
		t.Fatal("leaked raw error message to client")
	}
}

// TestWriteErrorWrappedAPIError wraps a domain error with contextual errors before serialization.
// It expects errors.As traversal to recover the original not-found status and contract.
// This allows services to add internal context without accidentally masking safe client errors.
func TestWriteErrorWrappedAPIError(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapped := errors.Join(errors.New("ctx"), domain.ErrNotFound("missing"))
	WriteError(rec, wrapped)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (errors.As should unwrap)", rec.Code)
	}
}
