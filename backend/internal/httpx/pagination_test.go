package httpx

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParsePage table-tests absent, valid, malformed, and out-of-range query limits plus opaque cursors.
// It expects deterministic defaults and clamps limits into the repository-safe range without interpreting the cursor.
// This bounds list work while keeping pagination tokens owned by the data layer.
func TestParsePage(t *testing.T) {
	cases := []struct {
		query      string
		wantLimit  int
		wantCursor string
	}{
		{"", DefaultLimit, ""},
		{"?limit=10&cursor=abc", 10, "abc"},
		{"?limit=0", MinLimit, ""},
		{"?limit=-5", MinLimit, ""},
		{"?limit=99999", MaxLimit, ""},
		{"?limit=notanumber", DefaultLimit, ""},
		{"?cursor=xyz", DefaultLimit, "xyz"},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/list"+c.query, nil)
			limit, cursor := ParsePage(r)
			if limit != c.wantLimit {
				t.Fatalf("limit = %d, want %d", limit, c.wantLimit)
			}
			if cursor != c.wantCursor {
				t.Fatalf("cursor = %q, want %q", cursor, c.wantCursor)
			}
		})
	}
}

// TestListEnvelope writes a populated page and a continuation cursor through the shared encoder.
// It expects both items and nextCursor in the documented list envelope.
// This keeps collection endpoints wire-compatible regardless of item type.
func TestListEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	ListEnvelope(rec, []string{"a", "b"}, "next123")
	var body struct {
		Data       []string `json:"data"`
		NextCursor string   `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Data) != 2 || body.NextCursor != "next123" {
		t.Fatalf("body = %+v", body)
	}
}

// TestListEnvelopeNilIsEmptyArray writes a terminal page from a nil Go slice and empty cursor.
// It expects data to encode as [] rather than null and nextCursor to be omitted.
// Stable array semantics spare clients from handling a second empty-list representation.
func TestListEnvelopeNilIsEmptyArray(t *testing.T) {
	rec := httptest.NewRecorder()
	ListEnvelope[string](rec, nil, "")
	if got := rec.Body.String(); !strings.Contains(got, `"data":[]`) {
		t.Fatalf("expected empty array, got %s", got)
	}
	if strings.Contains(rec.Body.String(), "nextCursor") {
		t.Fatalf("empty cursor should be omitted, got %s", rec.Body.String())
	}
}
