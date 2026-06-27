package httpx

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
)

// TestReadChatRouteRegistered walks the real router and asserts the chat
// mark-as-read endpoint is registered as POST. Proves a 404 against it is an
// environment/stale-binary issue, not a missing route.
func TestReadChatRouteRegistered(t *testing.T) {
	r := NewRouter(RouterConfig{Handlers: &handlers.Handlers{}})

	cr, ok := r.(chi.Routes)
	if !ok {
		t.Fatalf("router is not chi.Routes: %T", r)
	}

	var method, pattern string
	err := chi.Walk(cr, func(m, p string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasSuffix(p, "/chats/{cid}/read") {
			method, pattern = m, p
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if pattern == "" {
		t.Fatal("no /chats/{cid}/read route registered")
	}
	if method != http.MethodPost {
		t.Errorf("method = %s, want POST", method)
	}
	if pattern != "/api/v1/sessions/{session}/chats/{cid}/read" {
		t.Errorf("pattern = %q", pattern)
	}
}
