package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

type recordingCommittedConsumer struct {
	mu    sync.Mutex
	calls []string
	err   error
}

type handledCommittedConsumer struct {
	recordingCommittedConsumer
	handled bool
}

func (c *handledCommittedConsumer) HandleCommittedEvent(_ context.Context, event domain.Event) (bool, error) {
	c.calls = append(c.calls, event.ID)
	return c.handled, c.err
}

func (c *recordingCommittedConsumer) ConsumeCommittedEvent(_ context.Context, event domain.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, event.ID)
	return c.err
}

func (c *recordingCommittedConsumer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

type recordingCommittedPublisher struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (p *recordingCommittedPublisher) Publish(_ context.Context, event domain.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, event.ID)
	return p.err
}

func (p *recordingCommittedPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

type recordingCommittedWebhooks struct {
	calls []string
}

func (w *recordingCommittedWebhooks) Enqueue(_ context.Context, event domain.Event) (int, error) {
	w.calls = append(w.calls, event.ID)
	return 1, nil
}

type durableCommittedEventStore struct {
	mu        sync.Mutex
	committed map[string]domain.Event
	completed map[string]bool
	claims    []application.CommittedEventClaim
}

func (s *durableCommittedEventStore) ClaimCommittedEvents(_ context.Context, claim application.CommittedEventClaim) ([]domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims = append(s.claims, claim)
	out := make([]domain.Event, 0, claim.MaxItems)
	for id, event := range s.committed {
		if s.completed[id] {
			continue
		}
		out = append(out, event)
		if len(out) == claim.MaxItems {
			break
		}
	}
	return out, nil
}

func (s *durableCommittedEventStore) CompleteCommittedEvent(_ context.Context, _, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed[id] = true
	return nil
}

func newTestCommittedEventWorker(store application.CommittedEventWorkStore, dispatcher application.CommittedEventConsumer) *CommittedEventWorker {
	worker, err := NewCommittedEventWorker(store, dispatcher, CommittedEventWorkerConfig{
		Owner: "api-test",
		Lease: time.Minute,
		Batch: 8,
		Poll:  time.Second,
		Now:   func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		panic(err)
	}
	return worker
}

func committedEvent() domain.Event {
	return domain.Event{ID: "evt_01", Type: domain.EventMessage, Organization: "org_01", Session: "ses_01"}
}

func TestCommittedEventWorkerFansOutThenCompletes(t *testing.T) {
	projection := &recordingCommittedConsumer{}
	publisher := &recordingCommittedPublisher{}
	webhooks := &recordingCommittedWebhooks{}
	store := &durableCommittedEventStore{committed: map[string]domain.Event{"evt_01": committedEvent()}, completed: map[string]bool{}}
	worker := newTestCommittedEventWorker(store, NewCommittedEventDispatcher([]application.CommittedEventConsumer{projection}, publisher, webhooks))

	completed, err := worker.RunOnce(context.Background(), 1)
	if err != nil || completed != 1 {
		t.Fatalf("completed, err = %d, %v", completed, err)
	}
	if !store.completed["evt_01"] || projection.count() != 1 || publisher.count() != 1 || len(webhooks.calls) != 1 {
		t.Fatalf("completion=%v calls projection:%d publisher:%d webhooks:%d", store.completed, projection.count(), publisher.count(), len(webhooks.calls))
	}
	claim := store.claims[0]
	if !claim.LeaseUntil.Equal(claim.ClaimedAt.Add(time.Minute)) {
		t.Fatalf("claim clock and lease diverged: %+v", claim)
	}
}

type orderedCommittedStore struct {
	events    []domain.Event
	completed []string
}

func (s *orderedCommittedStore) ClaimCommittedEvents(context.Context, application.CommittedEventClaim) ([]domain.Event, error) {
	return s.events, nil
}

func (s *orderedCommittedStore) CompleteCommittedEvent(_ context.Context, _, id string) error {
	s.completed = append(s.completed, id)
	return nil
}

type selectiveCommittedConsumer struct {
	seen []string
	err  error
}

func (c *selectiveCommittedConsumer) ConsumeCommittedEvent(_ context.Context, event domain.Event) error {
	c.seen = append(c.seen, event.ID)
	if event.ID == "bad" {
		return c.err
	}
	return nil
}

func TestCommittedEventWorkerFailureDoesNotBlockUnrelatedSession(t *testing.T) {
	store := &orderedCommittedStore{events: []domain.Event{
		{ID: "bad", Session: "session_a"},
		{ID: "dependent", Session: "session_a"},
		{ID: "independent", Session: "session_b"},
	}}
	cause := errors.New("malformed external payload")
	consumer := &selectiveCommittedConsumer{err: cause}
	worker := newTestCommittedEventWorker(store, consumer)
	completed, err := worker.RunOnce(context.Background(), len(store.events))
	if completed != 1 || !errors.Is(err, cause) {
		t.Fatalf("completed=%d err=%v", completed, err)
	}
	if len(store.completed) != 1 || store.completed[0] != "independent" {
		t.Fatalf("failed work was lost: completed=%v", store.completed)
	}
	if len(consumer.seen) != 2 || consumer.seen[1] != "independent" {
		t.Fatalf("dependent work overtook failed event: seen=%v", consumer.seen)
	}
}

func TestCommittedEventWorkerFailureReplaysAfterRestart(t *testing.T) {
	publisher := &recordingCommittedPublisher{err: errors.New("redis unavailable")}
	store := &durableCommittedEventStore{committed: map[string]domain.Event{"evt_01": committedEvent()}, completed: map[string]bool{}}
	first := newTestCommittedEventWorker(store, NewCommittedEventDispatcher(nil, publisher, nil))
	if completed, err := first.RunOnce(context.Background(), 1); err == nil || completed != 0 {
		t.Fatalf("completed, err = %d, %v", completed, err)
	}
	if store.completed["evt_01"] {
		t.Fatal("failed event was completed")
	}

	publisher.mu.Lock()
	publisher.err = nil
	publisher.mu.Unlock()
	// A new worker models process restart; the durable store exposes the pending
	// committed event again because the prior worker never marked it complete.
	restarted := newTestCommittedEventWorker(store, NewCommittedEventDispatcher(nil, publisher, nil))
	if completed, err := restarted.RunOnce(context.Background(), 1); err != nil || completed != 1 {
		t.Fatalf("completed, err = %d, %v", completed, err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.completed["evt_01"] || publisher.count() != 2 {
		t.Fatalf("completion=%v publish calls=%d", store.completed, publisher.count())
	}
}

func TestCommittedEventWorkerDoesNotProcessBeforeCommittedClaim(t *testing.T) {
	publisher := &recordingCommittedPublisher{}
	store := &durableCommittedEventStore{committed: map[string]domain.Event{}, completed: map[string]bool{}}
	worker := newTestCommittedEventWorker(store, NewCommittedEventDispatcher(nil, publisher, nil))
	if completed, err := worker.RunOnce(context.Background(), 1); err != nil || completed != 0 {
		t.Fatalf("completed, err = %d, %v", completed, err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if publisher.count() != 0 || len(store.completed) != 0 || len(store.claims) != 1 {
		t.Fatalf("publisher calls=%d completions=%v claims=%d", publisher.count(), store.completed, len(store.claims))
	}
}

func TestCommittedEventDispatcherStopsFanoutForHandledEvent(t *testing.T) {
	login := &handledCommittedConsumer{handled: true}
	projection := &recordingCommittedConsumer{}
	publisher := &recordingCommittedPublisher{}
	webhooks := &recordingCommittedWebhooks{}
	store := &durableCommittedEventStore{
		committed: map[string]domain.Event{"evt_01": committedEvent()},
		completed: map[string]bool{},
	}
	worker := newTestCommittedEventWorker(
		store,
		NewCommittedEventDispatcher(
			[]application.CommittedEventConsumer{
				NewCommittedEventConsumers(login, projection),
			},
			publisher,
			webhooks,
		),
	)

	completed, err := worker.RunOnce(context.Background(), 1)
	if err != nil || completed != 1 {
		t.Fatalf("completed, err = %d, %v", completed, err)
	}
	if projection.count() != 0 || publisher.count() != 0 || len(webhooks.calls) != 0 {
		t.Fatalf("handled event leaked: projection=%d publisher=%d webhooks=%d", projection.count(), publisher.count(), len(webhooks.calls))
	}
}
