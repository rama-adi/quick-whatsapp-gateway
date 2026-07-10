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

	"github.com/stretchr/testify/require"

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

// TestDeliver_HappyPath_SignsAndMarksDelivered delivers a stored event through a webhook with an encrypted
// secret and one custom header. It verifies the exact JSON body, event-id and timestamp headers,
// HMAC-SHA512 over those same bytes, and the custom header seen by the HTTP client. A 200 response must
// produce one delivered transition with attempt 1 and response code 200.
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

// TestDeliver_NoSecret_NoHMACHeaders sends a webhook whose configuration has no signing secret. The
// request must omit both HMAC headers while still treating a 204 response as delivery success. This
// prevents an unsigned endpoint from receiving misleading algorithm metadata or an empty signature.
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

// TestDeliver_CustomHeadersCannotOverrideProtocolHeaders configures forged values for every protocol-owned
// header alongside a legitimate Authorization header. Delivery must replace the forged content type,
// request id, timestamp, algorithm, and signature with canonical values while preserving Authorization.
// This prevents webhook configuration from spoofing the integrity envelope.
func TestDeliver_CustomHeadersCannotOverrideProtocolHeaders(t *testing.T) {
	hook := domain.Webhook{
		ID: "wh_a", URL: "https://x.test/h", Events: []string{"*"},
		HMACSecret: []byte("cipher"),
		CustomHeaders: map[string]string{
			"Content-Type":      "text/plain",
			HeaderRequestID:     "spoofed",
			HeaderTimestamp:     "0",
			HeaderHMAC:          "forged",
			HeaderHMACAlgorithm: "none",
			"Authorization":     "Bearer endpoint-token",
		},
		RetryPolicy: domain.RetryPolicy{Attempts: 1},
	}
	wr, es := baseFixtures(hook)
	dr := &fakeDeliveryRepo{}
	doer := &fakeHTTPDoer{resp: resp(204, "")}
	d := newDispatcher(wr, dr, es, doer, &staticDecryptor{plaintext: []byte("secret")}, &fixedClock{ms: 12345})

	require.NoError(t, d.Deliver(context.Background(), domain.WebhookDelivery{ID: 1, WebhookID: hook.ID, EventID: "evt_001"}))
	req := doer.gotReq
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.Equal(t, "evt_001", req.Header.Get(HeaderRequestID))
	require.Equal(t, "12345", req.Header.Get(HeaderTimestamp))
	require.Equal(t, HMACAlgorithm, req.Header.Get(HeaderHMACAlgorithm))
	require.Equal(t, SignHMAC([]byte("secret"), doer.gotBody), req.Header.Get(HeaderHMAC))
	require.Equal(t, "Bearer endpoint-token", req.Header.Get("Authorization"))
}

// TestDeliver_FailureReschedulesWithBackoff starts from one prior attempt and returns HTTP 500 with a
// short diagnostic body. The dispatcher must record attempt 2 as failed, retain the response code and
// error text, and schedule the next try exactly four seconds after the fixed clock. This pins the
// one-based exponential retry calculation stored on delivery rows.
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

// TestDeliver_ExhaustionMarksDead returns a failing response on the third and final configured attempt.
// The row must move directly to dead with attempts=3 instead of receiving another next_retry_at. This
// keeps an exhausted endpoint from circulating forever through the worker queue.
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

// TestDeliver_TransportErrorIsFailure makes the HTTP client fail before a response exists. The dispatcher
// must persist a retryable failed attempt with no response code and include the connection error in
// last_error. Transport failures are delivery outcomes, not bookkeeping errors returned to the worker.
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

// TestDeliver_MissingWebhookDeadLetters claims a delivery after its webhook configuration has been
// deleted. Because no future retry can reconstruct the endpoint, the dispatcher must dead-letter
// immediately and never call the HTTP client. This prevents orphan rows from consuming the retry budget
// indefinitely.
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

// TestDeliver_HMACSecretButNoDecryptorFails loads a webhook that requires signing while the dispatcher has
// no decryptor. It must record a retryable failure and issue no HTTP request rather than silently sending
// an unsigned body. The retry keeps deployment misconfiguration visible without weakening authenticity.
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

// TestDeliverDue_ProcessesClaimed returns two due rows from one claim pass. Both rows must be attempted
// and independently marked delivered, and the method reports two processed items. This pins the batch
// contract: one row does not truncate the remainder of the claimed batch.
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

// TestDeliverDue_ClaimErrorPropagates makes the repository fail before any rows are claimed. DeliverDue
// must return that failure so the scheduler can retry the pass, with no delivery attempt made. Claim
// failures differ from per-row HTTP failures because no durable row ownership was obtained.
func TestDeliverDue_ClaimErrorPropagates(t *testing.T) {
	dr := &fakeDeliveryRepo{claimErr: errors.New("lock timeout")}
	d := newDispatcher(&fakeWebhookRepo{}, dr, &fakeEventStore{}, &fakeHTTPDoer{}, nil, &fixedClock{ms: 1})
	if _, err := d.DeliverDue(context.Background(), 10); err == nil {
		t.Fatal("expected claim error to propagate")
	}
}
