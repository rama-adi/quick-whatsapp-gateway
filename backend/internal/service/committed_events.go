package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
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
	return d.DispatchCommittedEvent(ctx, event, application.CommittedEventSourceGateway)
}

// DispatchCommittedEvent fans out one event. API-sourced events were already
// projected by their producer (the outbound scheduler records the sent
// message before appending its event), so they skip projections.
func (d *CommittedEventDispatcher) DispatchCommittedEvent(ctx context.Context, event domain.Event, source string) error {
	if d == nil {
		return errors.New("committed event dispatcher is nil")
	}
	if event.ID == "" {
		return errors.New("committed event id is required")
	}
	projections := d.projections
	if source == application.CommittedEventSourceAPI {
		projections = nil
	}
	for _, projection := range projections {
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

// committedEventFanout is the per-event fan-out attempt the worker drives.
type committedEventFanout interface {
	DispatchCommittedEvent(ctx context.Context, event domain.Event, source string) error
}

// CommittedEventWorker claims durable, already-committed events and drives
// each to done or dead. Every attempt is counted when it starts (so a crash
// mid-event still uses an attempt); a failure backs the event off
// exponentially and releases the rest of its session's claim, so the session
// waits behind it while unrelated sessions continue. A permanent failure, or
// the MaxAttempts-th failure, dead-letters the event and unblocks its session.
type CommittedEventWorker struct {
	store      application.CommittedEventWorkStore
	dispatcher committedEventFanout
	config     CommittedEventWorkerConfig
	log        *slog.Logger
	lastReport time.Time
}

// CommittedEventWorkerConfig is supplied by the composition root. It has no
// implicit timing or batch defaults because store leases must agree with the
// deployment's durability and replica policy.
type CommittedEventWorkerConfig struct {
	Owner       string
	Lease       time.Duration // per-event lease; renewed when each event starts
	Sessions    int           // sessions claimed per poll
	PerSession  int           // contiguous events claimed per session
	Poll        time.Duration
	MaxAttempts int
	BackoffBase time.Duration
	BackoffCap  time.Duration
	// ReportEvery is how often a non-empty backlog (oldest outstanding event,
	// dead count) is logged. Zero disables the report.
	ReportEvery time.Duration
	Now         func() time.Time
	Log         *slog.Logger
}

func NewCommittedEventWorker(
	store application.CommittedEventWorkStore,
	dispatcher committedEventFanout,
	config CommittedEventWorkerConfig,
) (*CommittedEventWorker, error) {
	if store == nil || dispatcher == nil {
		return nil, errors.New("committed event worker dependencies are required")
	}
	if config.Owner == "" {
		return nil, errors.New("committed event worker owner is required")
	}
	if config.Lease <= 0 || config.Sessions <= 0 || config.PerSession <= 0 || config.Poll <= 0 {
		return nil, errors.New("committed event worker lease, batch sizes, and poll must be positive")
	}
	if config.MaxAttempts <= 0 || config.BackoffBase <= 0 || config.BackoffCap < config.BackoffBase {
		return nil, errors.New("committed event worker attempts and backoff must be positive")
	}
	if config.Now == nil {
		return nil, errors.New("committed event worker clock is required")
	}
	log := config.Log
	if log == nil {
		log = slog.Default()
	}
	return &CommittedEventWorker{store: store, dispatcher: dispatcher, config: config, log: log}, nil
}

// RunOnce claims one batch and processes it session by session, in order.
// It returns how many events reached done or dead, and the joined errors of
// failed attempts (which are recorded on their events, not lost).
func (w *CommittedEventWorker) RunOnce(ctx context.Context) (int, error) {
	if w == nil || w.store == nil || w.dispatcher == nil {
		return 0, errors.New("committed event worker is not configured")
	}
	claimedAt := w.config.Now().UTC()
	token := domain.NewULID()
	events, err := w.store.ClaimCommittedEvents(ctx, application.CommittedEventClaim{
		Owner: w.config.Owner, Token: token, ClaimedAt: claimedAt, LeaseUntil: claimedAt.Add(w.config.Lease),
		MaxSessions: w.config.Sessions, MaxPerSession: w.config.PerSession,
	})
	if err != nil {
		return 0, fmt.Errorf("claim committed events: %w", err)
	}
	finished := 0
	stopped := map[string]bool{}
	var failures []error
	for _, item := range events {
		session := item.Event.Session
		if stopped[session] {
			continue
		}
		ok, err := w.process(ctx, token, item)
		if ok {
			finished++
		}
		if err != nil {
			failures = append(failures, err)
		}
		if !ok || err != nil {
			stopped[session] = true
			if releaseErr := w.store.ReleaseCommittedEvents(ctx, token, session); releaseErr != nil {
				failures = append(failures, fmt.Errorf("release session %s: %w", session, releaseErr))
			}
		}
	}
	return finished, errors.Join(failures...)
}

// process drives one claimed event. ok reports that it reached a terminal
// state (done, or dead which unblocks the session).
func (w *CommittedEventWorker) process(ctx context.Context, token string, item application.ClaimedCommittedEvent) (bool, error) {
	event := item.Event
	attempt := item.Attempts
	var cause error
	switch {
	case item.Missing:
		cause = application.PermanentEventError(errors.New("event body is missing from event_log"))
	case item.Attempts >= w.config.MaxAttempts:
		// Every attempt started but none recorded an outcome: the worker
		// crashed or lost its lease while processing this event.
		cause = application.PermanentEventError(fmt.Errorf(
			"abandoned after %d unfinished attempts; last error: %s", item.Attempts, item.LastError))
	default:
		now := w.config.Now().UTC()
		if err := w.store.StartCommittedEvent(ctx, token, event.ID, now.Add(w.config.Lease)); err != nil {
			return false, fmt.Errorf("start committed event %s: %w", event.ID, err)
		}
		attempt++
		cause = w.dispatcher.DispatchCommittedEvent(ctx, event, item.Source)
	}
	if cause == nil {
		if err := w.store.CompleteCommittedEvent(ctx, token, event.ID, w.config.Now().UTC()); err != nil {
			return false, fmt.Errorf("complete committed event %s: %w", event.ID, err)
		}
		return true, nil
	}
	if ctx.Err() != nil {
		// Shutdown is not the event's fault: leave the lease to expire.
		return false, ctx.Err()
	}
	dead := errors.Is(cause, application.ErrPermanentEvent) || attempt >= w.config.MaxAttempts
	failedAt := w.config.Now().UTC()
	failure := application.CommittedEventFailure{
		Token: token, EventID: event.ID, Error: cause.Error(), Dead: dead,
		NextAttemptAt: failedAt.Add(w.backoff(attempt)), At: failedAt,
	}
	if err := w.store.FailCommittedEvent(ctx, failure); err != nil {
		return false, fmt.Errorf("record committed event %s failure (%v): %w", event.ID, cause, err)
	}
	if dead {
		w.log.ErrorContext(ctx, "committed event dead-lettered; its session continues",
			"event_id", event.ID, "event_type", event.Type, "session_id", event.Session,
			"organization_id", event.Organization, "session_seq", item.SessionSeq,
			"attempts", attempt, "err", cause)
		return true, nil
	}
	return false, fmt.Errorf("dispatch committed event %s (attempt %d of %d): %w",
		event.ID, attempt, w.config.MaxAttempts, cause)
}

// backoff is BackoffBase doubled per attempt, capped at BackoffCap.
func (w *CommittedEventWorker) backoff(attempt int) time.Duration {
	d := w.config.BackoffBase
	for i := 1; i < attempt && d < w.config.BackoffCap; i++ {
		d *= 2
	}
	return min(d, w.config.BackoffCap)
}

// report logs the backlog when there is outstanding or dead work.
func (w *CommittedEventWorker) report(ctx context.Context) {
	if w.config.ReportEvery <= 0 {
		return
	}
	now := w.config.Now()
	if now.Sub(w.lastReport) < w.config.ReportEvery {
		return
	}
	w.lastReport = now
	backlog, err := w.store.CommittedEventBacklog(ctx)
	if err != nil {
		w.log.WarnContext(ctx, "committed event backlog unavailable", "err", err)
		return
	}
	if backlog.Outstanding == 0 && backlog.Dead == 0 {
		return
	}
	w.log.WarnContext(ctx, "committed event backlog",
		"outstanding", backlog.Outstanding, "dead", backlog.Dead,
		"oldest_event_id", backlog.OldestEventID, "oldest_session_id", backlog.OldestSessionID,
		"oldest_age", now.Sub(backlog.OldestCreatedAt).Round(time.Second),
		"oldest_attempts", backlog.OldestAttempts, "oldest_last_error", backlog.OldestLastError)
}

// Run polls durable work until ctx is cancelled. A full batch polls again at
// once; failures are logged and stay recorded on their events.
func (w *CommittedEventWorker) Run(ctx context.Context) error {
	if w == nil {
		return errors.New("committed event worker is nil")
	}
	for {
		finished, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.log.WarnContext(ctx, "committed event attempts failed; retrying with backoff", "err", err)
		}
		w.report(ctx)
		if finished > 0 && err == nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		timer := time.NewTimer(w.config.Poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
