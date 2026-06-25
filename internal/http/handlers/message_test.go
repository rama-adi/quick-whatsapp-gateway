package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

func newMessageHandlers(svc MessageSvc) *Handlers {
	return &Handlers{Messages: svc}
}

func TestSendMessage_SyncHappyPath(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync, WAMessageID: "WA1", Status: domain.MessageSent}}
	h := newMessageHandlers(svc)
	body := `{"type":"text","to":"628@s.whatsapp.net","text":"hi"}`
	r := withTenant(chiReq(http.MethodPost, "/api/v1/sessions/s1/messages", body, map[string]string{"session": "s1"}), testTenant)
	w := httptest.NewRecorder()
	h.SendMessage(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastReq.Type != domain.SendTypeText || svc.lastReq.Text != "hi" {
		t.Errorf("request not threaded: %+v", svc.lastReq)
	}
	var got outbound.SendResult
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.WAMessageID != "WA1" {
		t.Errorf("waMessageId = %q, want WA1", got.WAMessageID)
	}
}

func TestSendMessage_AsyncIs202_AndOptionsThreaded(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeAsync, OutboxID: "out_1"}}
	h := newMessageHandlers(svc)
	body := `{"type":"text","to":"628@s.whatsapp.net","text":"hi"}`
	r := chiReq(http.MethodPost, "/api/v1/sessions/s1/messages?async", body, map[string]string{"session": "s1"})
	r.Header.Set("Idempotency-Key", "key-1")
	r = withTenant(r, testTenant)
	w := httptest.NewRecorder()
	h.SendMessage(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	if !svc.lastOpts.Async {
		t.Error("Async option not set")
	}
	if svc.lastOpts.IdempotencyKey != "key-1" {
		t.Errorf("idempotency key = %q, want key-1", svc.lastOpts.IdempotencyKey)
	}
}

func TestSendMessage_NoTenant401(t *testing.T) {
	h := newMessageHandlers(&fakeMessageSvc{})
	r := chiReq(http.MethodPost, "/api/v1/sessions/s1/messages", `{"type":"text"}`, map[string]string{"session": "s1"})
	w := httptest.NewRecorder()
	h.SendMessage(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestSendMessage_ServiceValidationError(t *testing.T) {
	svc := &fakeMessageSvc{err: domain.ErrValidation("text is required")}
	h := newMessageHandlers(svc)
	r := withTenant(chiReq(http.MethodPost, "/api/v1/sessions/s1/messages", `{"type":"text","to":"x"}`, map[string]string{"session": "s1"}), testTenant)
	w := httptest.NewRecorder()
	h.SendMessage(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeValidationError {
		t.Errorf("code = %q, want %q", got, domain.CodeValidationError)
	}
}

func TestSendMessage_RateLimited429(t *testing.T) {
	svc := &fakeMessageSvc{err: domain.ErrRateLimited("slow down")}
	h := newMessageHandlers(svc)
	r := withTenant(chiReq(http.MethodPost, "/api/v1/sessions/s1/messages", `{"type":"text","to":"x","text":"y"}`, map[string]string{"session": "s1"}), testTenant)
	w := httptest.NewRecorder()
	h.SendMessage(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
}

func TestAddReaction_PassesEmoji(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync}}
	h := newMessageHandlers(svc)
	r := withTenant(chiReq(http.MethodPost, "/api/v1/sessions/s1/messages/m1/reaction", `{"chat":"c1","emoji":"👍"}`, map[string]string{"session": "s1", "mid": "m1"}), testTenant)
	w := httptest.NewRecorder()
	h.AddReaction(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastOp != "react:👍" {
		t.Errorf("op = %q, want react:👍", svc.lastOp)
	}
}

func TestRemoveReaction_ClearsEmoji(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync}}
	h := newMessageHandlers(svc)
	r := withTenant(chiReq(http.MethodDelete, "/api/v1/sessions/s1/messages/m1/reaction", `{"chat":"c1","emoji":"👍"}`, map[string]string{"session": "s1", "mid": "m1"}), testTenant)
	w := httptest.NewRecorder()
	h.RemoveReaction(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if svc.lastOp != "react:" {
		t.Errorf("op = %q, want react: (empty emoji)", svc.lastOp)
	}
}

func TestVoteMessage_Routes(t *testing.T) {
	svc := &fakeMessageSvc{result: outbound.SendResult{Mode: outbound.ModeSync}}
	h := newMessageHandlers(svc)
	r := withTenant(chiReq(http.MethodPost, "/api/v1/sessions/s1/messages/m1/vote", `{"chat":"c1","options":["A"]}`, map[string]string{"session": "s1", "mid": "m1"}), testTenant)
	w := httptest.NewRecorder()
	h.VoteMessage(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if svc.lastOp != "vote" {
		t.Errorf("op = %q, want vote", svc.lastOp)
	}
}
