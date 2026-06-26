package webhooks

import (
	"context"
	"errors"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func testEvent() domain.Event {
	return domain.Event{
		Schema:       domain.Schema,
		ID:           "evt_001",
		Type:         domain.EventMessage,
		Session:      "sess_1",
		Organization: "ten_1",
		Timestamp:    1000,
		Payload:      map[string]any{"hi": "there"},
	}
}

func TestEnqueue_CreatesPendingForMatching(t *testing.T) {
	evt := testEvent()
	wr := &fakeWebhookRepo{matching: map[string][]domain.Webhook{
		domain.EventMessage: {
			{ID: "wh_a", Events: []string{"*"}},
			{ID: "wh_b", Events: []string{domain.EventMessage}},
		},
	}}
	dr := &fakeDeliveryRepo{terminal: map[string]bool{}}
	clk := &fixedClock{ms: 5000}

	enq := NewEnqueuer(wr, dr, clk, nil)
	n, err := enq.Enqueue(context.Background(), evt)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if n != 2 || len(dr.created) != 2 {
		t.Fatalf("expected 2 deliveries, got n=%d created=%d", n, len(dr.created))
	}
	// Forwarded the right scope to the repo.
	if wr.lastOrganization != "ten_1" || wr.lastSession != "sess_1" || wr.lastType != domain.EventMessage {
		t.Errorf("repo scope wrong: %q/%q/%q", wr.lastOrganization, wr.lastSession, wr.lastType)
	}
	d := dr.created[0]
	if d.Status != domain.DeliveryPending || d.Attempts != 0 || d.CreatedAt != 5000 {
		t.Errorf("delivery fields wrong: %+v", d)
	}
	if d.NextRetryAt == nil || *d.NextRetryAt != 5000 {
		t.Errorf("NextRetryAt should be now (5000), got %v", d.NextRetryAt)
	}
}

func TestEnqueue_DedupSkipsTerminal(t *testing.T) {
	evt := testEvent()
	wr := &fakeWebhookRepo{matching: map[string][]domain.Webhook{
		domain.EventMessage: {
			{ID: "wh_done", Events: []string{"*"}},
			{ID: "wh_fresh", Events: []string{"*"}},
		},
	}}
	dr := &fakeDeliveryRepo{terminal: map[string]bool{
		deliveryKey("wh_done", "evt_001"): true,
	}}

	enq := NewEnqueuer(wr, dr, &fixedClock{ms: 1}, nil)
	n, err := enq.Enqueue(context.Background(), evt)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if n != 1 || len(dr.created) != 1 || dr.created[0].WebhookID != "wh_fresh" {
		t.Fatalf("dedup failed: n=%d created=%+v", n, dr.created)
	}
}

func TestEnqueue_DefensiveEventFilter(t *testing.T) {
	// Repo (loosely) returned a non-matching webhook; the guard must drop it.
	evt := testEvent()
	wr := &fakeWebhookRepo{matching: map[string][]domain.Webhook{
		domain.EventMessage: {
			{ID: "wh_other", Events: []string{domain.EventPollVote}},
		},
	}}
	dr := &fakeDeliveryRepo{terminal: map[string]bool{}}

	enq := NewEnqueuer(wr, dr, &fixedClock{ms: 1}, nil)
	n, _ := enq.Enqueue(context.Background(), evt)
	if n != 0 || len(dr.created) != 0 {
		t.Fatalf("expected 0 deliveries, got n=%d", n)
	}
}

func TestEnqueue_ListErrorPropagates(t *testing.T) {
	wr := &fakeWebhookRepo{listErr: errors.New("db down")}
	dr := &fakeDeliveryRepo{terminal: map[string]bool{}}
	enq := NewEnqueuer(wr, dr, &fixedClock{ms: 1}, nil)
	if _, err := enq.Enqueue(context.Background(), testEvent()); err == nil {
		t.Fatal("expected error from ListMatching failure")
	}
}

func TestEnqueue_CreateErrorSkipsButContinues(t *testing.T) {
	evt := testEvent()
	wr := &fakeWebhookRepo{matching: map[string][]domain.Webhook{
		domain.EventMessage: {{ID: "wh_a", Events: []string{"*"}}},
	}}
	dr := &fakeDeliveryRepo{terminal: map[string]bool{}, createErr: errors.New("insert failed")}
	enq := NewEnqueuer(wr, dr, &fixedClock{ms: 1}, nil)
	n, err := enq.Enqueue(context.Background(), evt)
	if err != nil {
		t.Fatalf("per-webhook create failure must not error the whole call: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 successful creates, got %d", n)
	}
}
