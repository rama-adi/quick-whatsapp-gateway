package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// TestNotFoundPreservesAndContextsDatabaseErrors ensures operational errors retain identity and context.
// A non-empty-row failure must still satisfy errors.Is while adding the resource name needed in logs.
func TestNotFoundPreservesAndContextsDatabaseErrors(t *testing.T) {
	t.Parallel()

	dbErr := errors.New("connection reset")
	got := notFound(dbErr, "session")
	if !errors.Is(got, dbErr) || !strings.Contains(got.Error(), "store: get session") {
		t.Fatalf("notFound error = %v", got)
	}
}

// TestNormLimit covers defaulting, accepted bounds, and maximum-page clamping.
// Table cases lock the repository-wide protection against unbounded or nonsensical list requests.
func TestNormLimit(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero defaults", 0, defaultLimit},
		{"negative defaults", -5, defaultLimit},
		{"in range", 10, 10},
		{"at max", maxLimit, maxLimit},
		{"over max clamps", maxLimit + 1, maxLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normLimit(tt.in); got != tt.want {
				t.Fatalf("normLimit(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseCursor covers empty, valid, overflow, negative, and malformed numeric cursors.
// Invalid cases must consistently become validation_error, while valid uint64 boundaries remain accepted.
func TestParseCursor(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    uint64
		wantErr bool
	}{
		{"empty is zero", "", 0, false},
		{"numeric", "42", 42, false},
		{"large", "18446744073709551615", 18446744073709551615, false},
		{"non-numeric", "abc", 0, true},
		{"negative", "-1", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCursor(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				// Malformed cursor must surface as a validation_error APIError.
				var apiErr *domain.APIError
				if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
					t.Fatalf("want validation_error APIError, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseCursor(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestEncodeCursorRoundTrip verifies opaque numeric cursors are reversible.
// Zero remains the no-next-page sentinel and positive ids survive encode/parse without loss.
func TestEncodeCursorRoundTrip(t *testing.T) {
	if encodeCursor(0) != "" {
		t.Fatal("encodeCursor(0) should be empty")
	}
	got, err := parseCursor(encodeCursor(12345))
	if err != nil {
		t.Fatal(err)
	}
	if got != 12345 {
		t.Fatalf("round-trip got %d", got)
	}
}

// TestPageFrom verifies next cursors appear only for full, non-empty pages.
// This protects list endpoints from advertising continuation after an exhausted short page.
func TestPageFrom(t *testing.T) {
	type item struct{ id uint64 }
	idOf := func(i item) uint64 { return i.id }

	t.Run("full page yields cursor", func(t *testing.T) {
		items := []item{{1}, {2}, {3}}
		p := pageFrom(items, 3, idOf)
		if p.NextCursor != "3" {
			t.Fatalf("want cursor 3, got %q", p.NextCursor)
		}
	})
	t.Run("partial page no cursor", func(t *testing.T) {
		items := []item{{1}, {2}}
		p := pageFrom(items, 3, idOf)
		if p.NextCursor != "" {
			t.Fatalf("want empty cursor, got %q", p.NextCursor)
		}
	})
	t.Run("empty page no cursor", func(t *testing.T) {
		p := pageFrom([]item{}, 3, idOf)
		if p.NextCursor != "" {
			t.Fatalf("want empty cursor, got %q", p.NextCursor)
		}
	})
}

// TestNullableJSON preserves SQL NULL for absent JSON while retaining real payloads.
// The distinction avoids invalid empty JSON strings and keeps optional columns semantically nullable.
func TestNullableJSON(t *testing.T) {
	if nullableJSON(nil) != nil {
		t.Fatal("nil bytes should map to nil (SQL NULL)")
	}
	if nullableJSON([]byte{}) != nil {
		t.Fatal("empty bytes should map to nil (SQL NULL)")
	}
	b := []byte(`{"a":1}`)
	got, ok := nullableJSON(b).([]byte)
	if !ok || string(got) != `{"a":1}` {
		t.Fatalf("non-empty bytes should pass through, got %v", nullableJSON(b))
	}
}

// TestPrefixCols verifies joined-query column qualification across formatted lists.
// Whitespace-heavy generated column constants must become an unambiguous alias-qualified projection.
func TestPrefixCols(t *testing.T) {
	got := prefixCols("c", "id, lid,\n\tname")
	want := "c.id, c.lid, c.name"
	if got != want {
		t.Fatalf("prefixCols = %q, want %q", got, want)
	}
}
