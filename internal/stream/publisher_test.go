package stream

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestPublisher_PublishFanOut(t *testing.T) {
	_, rc := newMiniRedis(t)
	pub := NewPublisher(rc, nil)

	// Subscriber on the exact organization+session channel.
	sub := rc.Subscribe(context.Background(), channelFor("ten_a", "sess_1"))
	defer sub.Close()
	if _, err := sub.Receive(context.Background()); err != nil { // wait for subscription
		t.Fatalf("subscribe confirm: %v", err)
	}
	ch := sub.Channel()

	e := domain.NewEvent(domain.EventMessage, "sess_1", "ten_a", map[string]any{"body": "hi"})
	if err := pub.Publish(context.Background(), e); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case msg := <-ch:
		var got domain.Event
		if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
			t.Fatalf("unmarshal delivered: %v", err)
		}
		if got.ID != e.ID || got.Type != domain.EventMessage || got.Organization != "ten_a" {
			t.Errorf("delivered event mismatch: %+v", got)
		}
		if got.Schema != domain.Schema {
			t.Errorf("schema = %q, want %q", got.Schema, domain.Schema)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered to subscriber")
	}
}

func TestPublisher_EmptyOrganizationRejected(t *testing.T) {
	_, rc := newMiniRedis(t)
	pub := NewPublisher(rc, nil)

	e := domain.NewEvent(domain.EventMessage, "sess_1", "", nil)
	if err := pub.Publish(context.Background(), e); err == nil {
		t.Fatal("expected error publishing event with empty organization")
	}
}
