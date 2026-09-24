package webhooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// Enqueuer is the WebhookEnqueuer the inbound pipeline (fan-out stage, §7) calls
// for every produced domain.Event. It looks up the webhooks matching the event's
// organization/session/type and persists one pending delivery row per webhook (skipping
// any that already have a terminal delivery for this event_id, for dedup). The
// actual HTTP send happens later in the Dispatcher worker loop.
type Enqueuer struct {
	webhooks   WebhookRepo
	deliveries WebhookDeliveryRepo
	clock      Clock
	log        *slog.Logger
}

// NewEnqueuer builds an Enqueuer. clock and log may be nil (a system clock and
// the slog default are used).
func NewEnqueuer(webhooks WebhookRepo, deliveries WebhookDeliveryRepo, clock Clock, log *slog.Logger) *Enqueuer {
	if clock == nil {
		clock = SystemClock()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Enqueuer{webhooks: webhooks, deliveries: deliveries, clock: clock, log: log}
}

// Enqueue persists pending deliveries for every webhook matching evt. It returns
// the number of deliveries created. Per-hook failures do not prevent other
// hooks from being scheduled, but are returned so the durable event can retry.
func (e *Enqueuer) Enqueue(ctx context.Context, evt domain.Event) (int, error) {
	hooks, err := e.webhooks.ListMatching(ctx, evt.Organization, evt.Session, evt.Type)
	if err != nil {
		return 0, fmt.Errorf("list matching webhooks: %w", err)
	}

	now := e.clock.NowMs()
	created := 0
	var failures []error
	for _, h := range hooks {
		// Defensive guard: the repo should already have filtered by events, but
		// re-check so a loose repo query can't fan out to unsubscribed hooks.
		if !EventMatches(h.Events, evt.Type) {
			continue
		}

		// Dedup: if a terminal delivery already exists for this webhook+event,
		// the event was already delivered or dead-lettered — don't re-enqueue.
		terminal, err := e.deliveries.ExistsTerminal(ctx, h.ID, evt.ID)
		if err != nil {
			e.log.WarnContext(ctx, "webhook dedup check failed; skipping",
				"webhook_id", h.ID, "event_id", evt.ID, "err", err)
			failures = append(failures, err)
			continue
		}
		if terminal {
			continue
		}

		d := &domain.WebhookDelivery{
			WebhookID: h.ID,
			EventID:   evt.ID,
			Status:    domain.DeliveryPending,
			Attempts:  0,
			// Due immediately on first attempt.
			NextRetryAt: &now,
			CreatedAt:   now,
		}
		if err := e.deliveries.Create(ctx, d); err != nil {
			e.log.WarnContext(ctx, "create webhook delivery failed; skipping",
				"webhook_id", h.ID, "event_id", evt.ID, "err", err)
			failures = append(failures, err)
			continue
		}
		created++
	}
	return created, errors.Join(failures...)
}
