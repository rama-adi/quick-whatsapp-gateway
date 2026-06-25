package httpx

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

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
