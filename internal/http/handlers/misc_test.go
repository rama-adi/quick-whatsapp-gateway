package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestEvents_DelegatesToStreamHandler(t *testing.T) {
	called := false
	h := &Handlers{EventStream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})}
	r := withOrganization(chiReq(http.MethodGet, "/api/v1/events", "", nil), testOrganization)
	w := httptest.NewRecorder()
	h.Events(w, r)
	if !called {
		t.Fatal("stream handler not invoked")
	}
}

func TestEvents_NilStream500(t *testing.T) {
	h := &Handlers{}
	r := withOrganization(chiReq(http.MethodGet, "/api/v1/events", "", nil), testOrganization)
	w := httptest.NewRecorder()
	h.Events(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
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
