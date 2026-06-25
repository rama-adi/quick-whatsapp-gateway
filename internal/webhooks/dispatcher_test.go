package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func resp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}
}

func newDispatcher(wr WebhookRepo, dr WebhookDeliveryRepo, es EventStore, h HTTPDoer, dec Decryptor, clk Clock) *Dispatcher {
	return NewDispatcher(wr, dr, es, h, dec, clk, nil)
}

func baseFixtures(hook domain.Webhook) (*fakeWebhookRepo, *fakeEventStore) {
	wr := &fakeWebhookRepo{byID: map[string]domain.Webhook{hook.ID: hook}}
	es := &fakeEventStore{events: map[string]domain.Event{"evt_001": testEvent()}}
	return wr, es
}

func TestDeliver_HappyPath_SignsAndMarksDelivered(t *testing.T) {
	secret := []byte("the-secret")
	hook := domain.Webhook{
		ID:            "wh_a",
		URL:           "https://example.test/hook",
		Events:        []string{"*"},
		HMACSecret:    []byte("ciphertext"), // decryptor returns `secret`
		CustomHeaders: map[string]string{"X-Custom": "yes"},
		RetryPolicy:   domain.RetryPolicy{Policy: "exponential", DelaySeconds: 2, Attempts: 15},
	}
	wr, es := baseFixtures(hook)
	dr := &fakeDeliveryRepo{}
	doer := &fakeHTTPDoer{resp: resp(200, "ok")}
	clk := &fixedClock{ms: 12345}

	d := newDispatcher(wr, dr, es, doer, &staticDecryptor{plaintext: secret}, clk)
	del := domain.WebhookDelivery{ID: 7, WebhookID: "wh_a", EventID: "evt_001", Attempts: 0}
	if err := d.Deliver(context.Background(), del); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if len(dr.calls) != 1 || dr.calls[0].kind != "delivered" {
		t.Fatalf("expected one delivered call, got %+v", dr.calls)
	}
	if dr.calls[0].attempts != 1 || dr.calls[0].responseCode == nil || *dr.calls[0].responseCode != 200 {
		t.Fatalf("delivered bookkeeping wrong: %+v", dr.calls[0])
	}

	req := doer.gotReq
	if req.Method != http.MethodPost || req.URL.String() != hook.URL {
		t.Fatalf("request wrong: %s %s", req.Method, req.URL)
	}
	if req.Header.Get(HeaderRequestID) != "evt_001" {
		t.Errorf("request id header = %q", req.Header.Get(HeaderRequestID))
	}
	if got := req.Header.Get(HeaderTimestamp); got != strconv.FormatInt(12345, 10) {
		t.Errorf("timestamp header = %q", got)
	}
	if req.Header.Get(HeaderHMACAlgorithm) != HMACAlgorithm {
		t.Errorf("algo header = %q", req.Header.Get(HeaderHMACAlgorithm))
	}
	if req.Header.Get("X-Custom") != "yes" {
		t.Errorf("custom header missing")
	}
	// Signature must be over the exact body the doer received.
	wantSig := SignHMAC(secret, doer.gotBody)
	if got := req.Header.Get(HeaderHMAC); got != wantSig {
		t.Errorf("hmac header = %q, want %q", got, wantSig)
	}
	// Body must be the marshaled event.
	var got domain.Event
	if err := json.Unmarshal(doer.gotBody, &got); err != nil {
		t.Fatalf("body not valid event json: %v", err)
	}
	if got.ID != "evt_001" {
		t.Errorf("body event id = %q", got.ID)
	}
}

func TestDeliver_NoSecret_NoHMACHeaders(t *testing.T) {
	hook := domain.Webhook{ID: "wh_a", URL: "https://x.test/h", Events: []string{"*"},
		RetryPolicy: domain.RetryPolicy{Policy: "exponential", DelaySeconds: 2, Attempts: 15}}
	wr, es := baseFixtures(hook)
	dr := &fakeDeliveryRepo{}
	doer := &fakeHTTPDoer{resp: resp(204, "")}

	d := newDispatcher(wr, dr, es, doer, nil, &fixedClock{ms: 1})
	del := domain.WebhookDelivery{ID: 1, WebhookID: "wh_a", EventID: "evt_001"}
	if err := d.Deliver(context.Background(), del); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if doer.gotReq.Header.Get(HeaderHMAC) != "" || doer.gotReq.Header.Get(HeaderHMACAlgorithm) != "" {
		t.Fatal("hmac headers must be absent when no secret set")
	}
	if dr.calls[0].kind != "delivered" {
		t.Fatalf("expected delivered, got %s", dr.calls[0].kind)
	}
}

func TestDeliver_FailureReschedulesWithBackoff(t *testing.T) {
	hook := domain.Webhook{ID: "wh_a", URL: "https://x.test/h", Events: []string{"*"},
		RetryPolicy: domain.RetryPolicy{Policy: "exponential", DelaySeconds: 2, Attempts: 5}}
	wr, es := baseFixtures(hook)
	dr := &fakeDeliveryRepo{}
	doer := &fakeHTTPDoer{resp: resp(500, "boom")}
	clk := &fixedClock{ms: 100000}

	d := newDispatcher(wr, dr, es, doer, nil, clk)
	// Already 1 prior attempt -> this is attempt 2 -> next delay = 2*2^(2-1)=4s.
	del := domain.WebhookDelivery{ID: 9, WebhookID: "wh_a", EventID: "evt_001", Attempts: 1}
	if err := d.Deliver(context.Background(), del); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	c := dr.calls[0]
	if c.kind != "failed" {
		t.Fatalf("expected failed, got %s", c.kind)
	}
	if c.attempts != 2 {
		t.Errorf("attempts = %d, want 2", c.attempts)
	}
	wantNext := int64(100000) + 4*1000
	if c.nextRetryAt != wantNext {
		t.Errorf("nextRetryAt = %d, want %d", c.nextRetryAt, wantNext)
	}
	if c.responseCode == nil || *c.responseCode != 500 {
		t.Errorf("response code not captured: %+v", c.responseCode)
	}
	if !strings.Contains(c.lastErr, "500") {
		t.Errorf("lastErr should mention status: %q", c.lastErr)
	}
}

func TestDeliver_ExhaustionMarksDead(t *testing.T) {
	hook := domain.Webhook{ID: "wh_a", URL: "https://x.test/h", Events: []string{"*"},
		RetryPolicy: domain.RetryPolicy{Policy: "exponential", DelaySeconds: 2, Attempts: 3}}
	wr, es := baseFixtures(hook)
	dr := &fakeDeliveryRepo{}
	doer := &fakeHTTPDoer{resp: resp(503, "down")}

	d := newDispatcher(wr, dr, es, doer, nil, &fixedClock{ms: 1})
	// Attempts=2 -> this is attempt 3 -> reaches the budget -> dead.
	del := domain.WebhookDelivery{ID: 4, WebhookID: "wh_a", EventID: "evt_001", Attempts: 2}
	if err := d.Deliver(context.Background(), del); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if dr.calls[0].kind != "dead" || dr.calls[0].attempts != 3 {
		t.Fatalf("expected dead at attempt 3, got %+v", dr.calls[0])
	}
}

func TestDeliver_TransportErrorIsFailure(t *testing.T) {
	hook := domain.Webhook{ID: "wh_a", URL: "https://x.test/h", Events: []string{"*"},
		RetryPolicy: domain.RetryPolicy{Policy: "exponential", DelaySeconds: 1, Attempts: 5}}
	wr, es := baseFixtures(hook)
	dr := &fakeDeliveryRepo{}
	doer := &fakeHTTPDoer{err: errors.New("connection refused")}

	d := newDispatcher(wr, dr, es, doer, nil, &fixedClock{ms: 0})
	del := domain.WebhookDelivery{ID: 1, WebhookID: "wh_a", EventID: "evt_001", Attempts: 0}
	if err := d.Deliver(context.Background(), del); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	c := dr.calls[0]
	if c.kind != "failed" || c.responseCode != nil {
		t.Fatalf("transport error: expected failed with nil code, got %+v", c)
	}
	if !strings.Contains(c.lastErr, "connection refused") {
		t.Errorf("lastErr = %q", c.lastErr)
	}
}

func TestDeliver_MissingWebhookDeadLetters(t *testing.T) {
	wr := &fakeWebhookRepo{byID: map[string]domain.Webhook{}} // empty -> not found
	es := &fakeEventStore{events: map[string]domain.Event{"evt_001": testEvent()}}
	dr := &fakeDeliveryRepo{}
	doer := &fakeHTTPDoer{resp: resp(200, "ok")}

	d := newDispatcher(wr, dr, es, doer, nil, &fixedClock{ms: 1})
	del := domain.WebhookDelivery{ID: 1, WebhookID: "wh_gone", EventID: "evt_001"}
	if err := d.Deliver(context.Background(), del); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if dr.calls[0].kind != "dead" {
		t.Fatalf("missing webhook should dead-letter, got %s", dr.calls[0].kind)
	}
	if doer.gotReq != nil {
		t.Fatal("should not have issued an HTTP request for a missing webhook")
	}
}

func TestDeliver_HMACSecretButNoDecryptorFails(t *testing.T) {
	hook := domain.Webhook{ID: "wh_a", URL: "https://x.test/h", Events: []string{"*"},
		HMACSecret:  []byte("cipher"),
		RetryPolicy: domain.RetryPolicy{Policy: "exponential", DelaySeconds: 1, Attempts: 5}}
	wr, es := baseFixtures(hook)
	dr := &fakeDeliveryRepo{}
	doer := &fakeHTTPDoer{resp: resp(200, "ok")}

	d := newDispatcher(wr, dr, es, doer, nil /* no decryptor */, &fixedClock{ms: 1})
	del := domain.WebhookDelivery{ID: 1, WebhookID: "wh_a", EventID: "evt_001"}
	if err := d.Deliver(context.Background(), del); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if dr.calls[0].kind != "failed" {
		t.Fatalf("missing decryptor should fail (and retry), got %s", dr.calls[0].kind)
	}
	if doer.gotReq != nil {
		t.Fatal("should not POST when request build fails")
	}
}

func TestDeliverDue_ProcessesClaimed(t *testing.T) {
	hook := domain.Webhook{ID: "wh_a", URL: "https://x.test/h", Events: []string{"*"},
		RetryPolicy: domain.RetryPolicy{Policy: "exponential", DelaySeconds: 2, Attempts: 5}}
	wr, es := baseFixtures(hook)
	dr := &fakeDeliveryRepo{due: []domain.WebhookDelivery{
		{ID: 1, WebhookID: "wh_a", EventID: "evt_001"},
		{ID: 2, WebhookID: "wh_a", EventID: "evt_001"},
	}}
	doer := &fakeHTTPDoer{resp: resp(200, "ok")}

	d := newDispatcher(wr, dr, es, doer, nil, &fixedClock{ms: 1})
	n, err := d.DeliverDue(context.Background(), 0)
	if err != nil {
		t.Fatalf("DeliverDue: %v", err)
	}
	if n != 2 || len(dr.calls) != 2 {
		t.Fatalf("expected 2 processed, got n=%d calls=%d", n, len(dr.calls))
	}
}

func TestDeliverDue_ClaimErrorPropagates(t *testing.T) {
	dr := &fakeDeliveryRepo{claimErr: errors.New("lock timeout")}
	d := newDispatcher(&fakeWebhookRepo{}, dr, &fakeEventStore{}, &fakeHTTPDoer{}, nil, &fixedClock{ms: 1})
	if _, err := d.DeliverDue(context.Background(), 10); err == nil {
		t.Fatal("expected claim error to propagate")
	}
}
