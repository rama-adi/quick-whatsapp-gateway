package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
)

func TestAdmissionBlocksMutationsButAllowsReads(t *testing.T) {
	gate := NewAdmissionGate(false)
	router := NewRouter(RouterConfig{
		Handlers:  &handlers.Handlers{},
		Admission: gate,
	})
	for _, test := range []struct {
		method, path string
		blocked      bool
	}{
		{method: http.MethodPost, path: "/api/v1/sessions/s1/chats/c1/read", blocked: true},
		{method: http.MethodDelete, path: "/api/v1/webhooks/w1", blocked: true},
		{method: http.MethodGet, path: "/api/v1/sessions/s1", blocked: true},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if test.blocked && response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", test.method, response.Code)
		}
		if !test.blocked && response.Code == http.StatusServiceUnavailable {
			t.Errorf("%s was blocked", test.method)
		}
	}
	gate.SetOpen(true)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s1/chats/c1/read", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code == http.StatusServiceUnavailable {
		t.Fatal("open admission blocked mutation")
	}
}

func TestAdmissionCloseWaitsForInflight(t *testing.T) {
	gate := NewAdmissionGate(true)
	leave, ok := gate.enter()
	if !ok {
		t.Fatal("open gate rejected request")
	}
	done := make(chan error, 1)
	go func() { done <- gate.CloseAndWait(context.Background()) }()
	select {
	case <-done:
		t.Fatal("drain completed with request in flight")
	case <-time.After(10 * time.Millisecond):
	}
	leave()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok = gate.enter(); ok {
		t.Fatal("closed gate admitted request")
	}
	gate.SetOpen(true)
	if _, ok = gate.enter(); ok {
		t.Fatal("terminally closed gate reopened")
	}
}
