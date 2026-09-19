package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/journal"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/inbound"
)

// controlEventSink is the control-mode event boundary. An event is accepted
// only when the reconciler still owns its session; the durable journal then
// replaces the legacy Redis/webhook fan-out until the API commits it.
type controlEventSink struct {
	adapter    *journal.ControlAdapter
	assignment func(organizationID, sessionID string) (uint64, bool)
	log        *slog.Logger
}

func (s controlEventSink) append(ctx context.Context, event domain.Event) error {
	epoch, ok := s.assignment(event.Organization, event.Session)
	if !ok {
		return errors.New("event session is not currently assigned")
	}
	_, _, err := s.adapter.AppendDomainEvent(ctx, event, epoch)
	return err
}

func (s controlEventSink) Publish(ctx context.Context, event domain.Event) error {
	return s.append(ctx, event)
}

// PublishManaged satisfies wa.EventSink, whose callback cannot return an
// error. It still records append failures at the composition boundary.
func (s controlEventSink) PublishManaged(ctx context.Context, event domain.Event) {
	if err := s.append(ctx, event); err != nil {
		s.log.WarnContext(ctx, "journal event append failed", "event_id", event.ID, "type", event.Type, "err", err)
	}
}

type managedControlEventSink struct{ controlEventSink }

func (s managedControlEventSink) Publish(ctx context.Context, event domain.Event) {
	s.PublishManaged(ctx, event)
}

var _ wa.EventSink = managedControlEventSink{}
var _ inbound.EventSink = controlEventSink{}

// controlWebhookSink intentionally makes the journal the sole control-mode
// handoff. The API owns downstream webhook and realtime delivery after commit.
type controlWebhookSink struct{}

func (controlWebhookSink) Enqueue(context.Context, domain.Event) error { return nil }

var _ inbound.WebhookEnqueuer = controlWebhookSink{}
