package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestParam_DecodesEncodedJID(t *testing.T) {
	// chi returns the still-escaped path segment when URL.RawPath is set; param
	// must URL-decode it so a JID like "120363@g.us" (arriving as "120363%40g.us")
	// matches the stored value.
	r := chiReq(http.MethodGet, "/", "", map[string]string{
		"cid":     "120363025249719889%40g.us",
		"session": "sess_plain",
	})
	if got := param(r, "cid"); got != "120363025249719889@g.us" {
		t.Errorf("cid = %q, want decoded @", got)
	}
	if got := param(r, "session"); got != "sess_plain" {
		t.Errorf("session = %q, want unchanged", got)
	}
}

func TestAdminListSessions_HappyPath(t *testing.T) {
	h := &Handlers{Admin: &fakeAdminSvc{list: []domain.WASession{{ID: "sess_1"}, {ID: "sess_2"}}}}
	r := chiReq(http.MethodGet, "/api/v1/admin/sessions", "", nil)
	w := httptest.NewRecorder()
	h.AdminListSessions(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
