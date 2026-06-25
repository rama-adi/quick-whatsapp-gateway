package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
)

func newKeyHandlers(svc KeySvc) *Handlers { return &Handlers{Keys: svc} }

func TestCreateKey_ReturnsFullKeyOnce(t *testing.T) {
	svc := &fakeKeySvc{createRes: service.CreateKeyResult{
		Key:     domain.APIKey{ID: "wak_1", KeyPrefix: "wak_ab12"},
		FullKey: "wak_secretfull",
	}}
	h := newKeyHandlers(svc)
	r := withTenant(chiReq(http.MethodPost, "/api/v1/keys", `{"name":"ci","permissions":{"send":true}}`, nil), testTenant)
	w := httptest.NewRecorder()
	h.CreateKey(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var got service.CreateKeyResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.FullKey != "wak_secretfull" {
		t.Errorf("fullKey = %q, want wak_secretfull", got.FullKey)
	}
}

func TestCreateKey_NoTenant401(t *testing.T) {
	h := newKeyHandlers(&fakeKeySvc{})
	r := chiReq(http.MethodPost, "/api/v1/keys", `{"name":"x"}`, nil)
	w := httptest.NewRecorder()
	h.CreateKey(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestListKeys_HidesHash(t *testing.T) {
	svc := &fakeKeySvc{list: []domain.APIKey{{ID: "wak_1", KeyHash: "argon2id$secret"}}}
	h := newKeyHandlers(svc)
	r := withTenant(chiReq(http.MethodGet, "/api/v1/keys", "", nil), testTenant)
	w := httptest.NewRecorder()
	h.ListKeys(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// KeyHash has json:"-"; it must never appear in the response body.
	if contains(w.Body.String(), "argon2id") {
		t.Errorf("response leaked key hash: %s", w.Body.String())
	}
}

func TestDeleteKey_NoContent(t *testing.T) {
	h := newKeyHandlers(&fakeKeySvc{})
	r := withTenant(chiReq(http.MethodDelete, "/api/v1/keys/wak_1", "", map[string]string{"id": "wak_1"}), testTenant)
	w := httptest.NewRecorder()
	h.DeleteKey(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}

func TestRotateKey_ReturnsNewFullKey(t *testing.T) {
	svc := &fakeKeySvc{rotateRes: service.CreateKeyResult{FullKey: "wak_rotated"}}
	h := newKeyHandlers(svc)
	r := withTenant(chiReq(http.MethodPost, "/api/v1/keys/wak_1:rotate", "", map[string]string{"id": "wak_1"}), testTenant)
	w := httptest.NewRecorder()
	h.RotateKey(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got service.CreateKeyResult
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.FullKey != "wak_rotated" {
		t.Errorf("fullKey = %q, want wak_rotated", got.FullKey)
	}
}

func TestGetKey_NotFound(t *testing.T) {
	svc := &fakeKeySvc{err: domain.ErrNotFound("api key not found")}
	h := newKeyHandlers(svc)
	r := withTenant(chiReq(http.MethodGet, "/api/v1/keys/x", "", map[string]string{"id": "x"}), testTenant)
	w := httptest.NewRecorder()
	h.GetKey(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
