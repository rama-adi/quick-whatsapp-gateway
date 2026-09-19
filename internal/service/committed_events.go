package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// CommittedEventConsumers is a thread-safe fan-out list. The API starts its
// durable worker before optional private-gateway wiring is complete; consumers
// that depend on that wiring can be added before the first normal event flow.
type CommittedEventConsumers struct {
	mu        sync.RWMutex
	consumers []application.CommittedEventConsumer
}

type handledCommittedEventConsumer interface {
	HandleCommittedEvent(context.Context, domain.Event) (bool, error)
}

// NewCommittedEventConsumers creates a fan-out list with the supplied sinks.
func NewCommittedEventConsumers(consumers ...application.CommittedEventConsumer) *CommittedEventConsumers {
	out := &CommittedEventConsumers{consumers: make([]application.CommittedEventConsumer, 0, len(consumers))}
	for _, consumer := range consumers {
		if consumer != nil {
			out.consumers = append(out.consumers, consumer)
		}
	}
	return out
}

// Add appends a consumer for subsequent events.
func (c *CommittedEventConsumers) Add(consumer application.CommittedEventConsumer) {
	if c == nil || consumer == nil {
		return
	}
	c.mu.Lock()
	c.consumers = append(c.consumers, consumer)
	c.mu.Unlock()
}

func (c *CommittedEventConsumers) ConsumeCommittedEvent(ctx context.Context, event domain.Event) error {
	_, err := c.HandleCommittedEvent(ctx, event)
	return err
}

// HandleCommittedEvent runs the fan-out list and reports whether a consumer
// claimed the event exclusively (used by OAuth login messages).
func (c *CommittedEventConsumers) HandleCommittedEvent(ctx context.Context, event domain.Event) (bool, error) {
	if c == nil {
		return false, nil
	}
	c.mu.RLock()
	consumers := append([]application.CommittedEventConsumer(nil), c.consumers...)
	c.mu.RUnlock()
	for _, consumer := range consumers {
		if handled, ok := consumer.(handledCommittedEventConsumer); ok {
			consumed, err := handled.HandleCommittedEvent(ctx, event)
			if err != nil {
				return false, err
			}
			if consumed {
				return true, nil
			}
			continue
		}
		if err := consumer.ConsumeCommittedEvent(ctx, event); err != nil {
			return false, err
		}
	}
	return false, nil
}

// committedEventPublisher and committedEventWebhookEnqueuer describe the two
// existing event consumers without making application depend on Redis or the
// webhook package.
type committedEventPublisher interface {
	Publish(context.Context, domain.Event) error
}

type committedEventWebhookEnqueuer interface {
	Enqueue(context.Context, domain.Event) (int, error)
}

// CommittedEventDispatcher performs one post-commit fan-out attempt. It keeps
// no delivery state: durable claiming and completion are the worker/store
// boundary, while consumers use event IDs for their own idempotency.
type CommittedEventDispatcher struct {
	projections []application.CommittedEventConsumer
	publisher   committedEventPublisher
	webhooks    committedEventWebhookEnqueuer
}

// NewCommittedEventDispatcher builds the transport-independent fan-out attempt.
// Projections run before external consumers; Redis publication is retained
// during the gateway-to-API transition, followed by webhook delivery enqueueing.
func NewCommittedEventDispatcher(
	projections []application.CommittedEventConsumer,
	publisher committedEventPublisher,
	webhooks committedEventWebhookEnqueuer,
) *CommittedEventDispatcher {
	activeProjections := make([]application.CommittedEventConsumer, 0, len(projections))
	for _, projection := range projections {
		if projection != nil {
			activeProjections = append(activeProjections, projection)
		}
	}
	return &CommittedEventDispatcher{projections: activeProjections, publisher: publisher, webhooks: webhooks}
}

var _ application.CommittedEventConsumer = (*CommittedEventDispatcher)(nil)

func (d *CommittedEventDispatcher) ConsumeCommittedEvent(ctx context.Context, event domain.Event) error {
	if d == nil {
		return errors.New("committed event dispatcher is nil")
	}
	if event.ID == "" {
		return errors.New("committed event id is required")
	}
	for _, projection := range d.projections {
		if handled, ok := projection.(handledCommittedEventConsumer); ok {
			consumed, err := handled.HandleCommittedEvent(ctx, event)
			if err != nil {
				return fmt.Errorf("consume committed event: %w", err)
			}
			if consumed {
				return nil
			}
			continue
		}
		if err := projection.ConsumeCommittedEvent(ctx, event); err != nil {
			return fmt.Errorf("project committed event: %w", err)
		}
	}
	if d.publisher != nil {
		if err := d.publisher.Publish(ctx, event); err != nil {
			return fmt.Errorf("publish committed event: %w", err)
		}
	}
	if d.webhooks != nil {
		if _, err := d.webhooks.Enqueue(ctx, event); err != nil {
			return fmt.Errorf("enqueue committed event webhooks: %w", err)
		}
	}
	return nil
}

// CommittedEventWorker claims durable, already-committed envelopes and marks
// them complete only after the dispatcher succeeds. A failed consumer leaves
// completion untouched, so the store's claim lease makes the envelope
// retryable after a worker crash or ordinary failure.
type CommittedEventWorker struct {
	store      application.CommittedEventWorkStore
	dispatcher application.CommittedEventConsumer
	config     CommittedEventWorkerConfig
}

// CommittedEventWorkerConfig is supplied by the composition root. It has no
// implicit timing or batch defaults because store leases must agree with the
// deployment's durability and replica policy.
type CommittedEventWorkerConfig struct {
	Owner string
	Lease time.Duration
	Batch int
	Poll  time.Duration
	Now   func() time.Time
}

func NewCommittedEventWorker(
	store application.CommittedEventWorkStore,
	dispatcher application.CommittedEventConsumer,
	config CommittedEventWorkerConfig,
) (*CommittedEventWorker, error) {
	if store == nil || dispatcher == nil {
		return nil, errors.New("committed event worker dependencies are required")
	}
	if config.Owner == "" {
		return nil, errors.New("committed event worker owner is required")
	}
	if config.Lease <= 0 || config.Batch <= 0 || config.Poll <= 0 {
		return nil, errors.New("committed event worker lease, batch, and poll must be positive")
	}
	if config.Now == nil {
		return nil, errors.New("committed event worker clock is required")
	}
	return &CommittedEventWorker{store: store, dispatcher: dispatcher, config: config}, nil
}

// RunOnce claims up to maxItems entries. It stops at the first failed dispatch
// or completion write so an unfinished claimed event remains available for the
// durable store's retry path. It returns the number marked complete.
func (w *CommittedEventWorker) RunOnce(ctx context.Context, maxItems int) (int, error) {
	if w == nil || w.store == nil || w.dispatcher == nil {
		return 0, errors.New("committed event worker is not configured")
	}
	if maxItems <= 0 {
		return 0, errors.New("committed event worker max items must be positive")
	}
	claimedAt := w.config.Now().UTC()
	events, err := w.store.ClaimCommittedEvents(ctx, application.CommittedEventClaim{
		Owner: w.config.Owner, LeaseUntil: claimedAt.Add(w.config.Lease), MaxItems: maxItems,
	})
	if err != nil {
		return 0, fmt.Errorf("claim committed events: %w", err)
	}
	completed := 0
	for _, event := range events {
		if event.ID == "" {
			return completed, errors.New("claimed committed event id is required")
		}
		if err := w.dispatcher.ConsumeCommittedEvent(ctx, event); err != nil {
			return completed, fmt.Errorf("dispatch committed event %s: %w", event.ID, err)
		}
		if err := w.store.CompleteCommittedEvent(ctx, w.config.Owner, event.ID); err != nil {
			return completed, fmt.Errorf("complete committed event %s: %w", event.ID, err)
		}
		completed++
	}
	return completed, nil
}

// Run polls durable work until ctx is cancelled. Failed attempts remain
// incomplete and are retried on later polls after the store makes their claim
// available again. Run returns only context cancellation; callers that need
// per-attempt diagnostics should wrap the work store or dispatcher.
func (w *CommittedEventWorker) Run(ctx context.Context) error {
	if w == nil {
		return errors.New("committed event worker is nil")
	}
	ticker := time.NewTicker(w.config.Poll)
	defer ticker.Stop()
	for {
		_, _ = w.RunOnce(ctx, w.config.Batch)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
