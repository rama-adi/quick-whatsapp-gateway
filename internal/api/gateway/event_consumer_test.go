package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

type committedEventDispatcherStub struct {
	event domain.Event
	err   error
}

func (s *committedEventDispatcherStub) ConsumeCommittedEvent(_ context.Context, event domain.Event) error {
	s.event = event
	return s.err
}

func TestCommittedEventConsumerDelegatesOnlyToConfiguredDispatcher(t *testing.T) {
	event := domain.Event{ID: "evt_01"}
	stub := &committedEventDispatcherStub{}
	consumer := CommittedEventConsumer{Dispatcher: stub}
	if err := consumer.ConsumeCommittedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if stub.event.ID != event.ID {
		t.Fatalf("event = %+v", stub.event)
	}
	stub.err = errors.New("fanout failed")
	if err := consumer.ConsumeCommittedEvent(context.Background(), event); !errors.Is(err, stub.err) {
		t.Fatalf("err = %v", err)
	}
}

func TestCommittedEventConsumerFailsClosedWithoutDispatcher(t *testing.T) {
	if err := (CommittedEventConsumer{}).ConsumeCommittedEvent(context.Background(), domain.Event{ID: "evt_01"}); err == nil {
		t.Fatal("expected configuration error")
	}
}
