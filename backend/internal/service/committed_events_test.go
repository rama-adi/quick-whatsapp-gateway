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

// memoryFanoutStore models event_fanout: per-session sequence order, only a
// session's head is claimable, tokens fence every write, dead unblocks.
type memoryFanoutStore struct {
	mu   sync.Mutex
	now  func() time.Time
	rows []*memoryFanoutRow
}

type memoryFanoutRow struct {
	event     domain.Event
	source    string
	seq       uint64
	status    string
	attempts  int
	next      time.Time
	token     string
	lease     time.Time
	lastError string
}

func newMemoryFanoutStore(now func() time.Time, events ...domain.Event) *memoryFanoutStore {
	s := &memoryFanoutStore{now: now}
	seqs := map[string]uint64{}
	for _, e := range events {
		seqs[e.Session]++
		s.rows = append(s.rows, &memoryFanoutRow{event: e, source: application.CommittedEventSourceGateway,
			seq: seqs[e.Session], status: "pending"})
	}
	return s
}

func (s *memoryFanoutStore) row(id string) *memoryFanoutRow {
	for _, r := range s.rows {
		if r.event.ID == id {
			return r
		}
	}
	return nil
}

func (s *memoryFanoutStore) ClaimCommittedEvents(_ context.Context, c application.CommittedEventClaim) ([]application.ClaimedCommittedEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	open := func(r *memoryFanoutRow) bool { return r.status == "pending" || r.status == "leased" }
	free := func(r *memoryFanoutRow) bool {
		return r.status == "pending" || (r.status == "leased" && !r.lease.After(c.ClaimedAt))
	}
	var out []application.ClaimedCommittedEvent
	sessions := 0
	seen := map[string]bool{}
	for _, head := range s.rows {
		if !open(head) || seen[head.event.Session] {
			continue
		}
		seen[head.event.Session] = true // first open row in seq order is the head
		if !free(head) || head.next.After(c.ClaimedAt) || sessions == c.MaxSessions {
			continue
		}
		sessions++
		taken := 0
		for _, r := range s.rows {
			if r.event.Session != head.event.Session || r.seq < head.seq || taken == c.MaxPerSession {
				continue
			}
			if !free(r) {
				break
			}
			r.status, r.token, r.lease = "leased", c.Token, c.LeaseUntil
			out = append(out, application.ClaimedCommittedEvent{Event: r.event, Source: r.source,
				SessionSeq: r.seq, Attempts: r.attempts, LastError: r.lastError})
			taken++
		}
	}
	return out, nil
}

func (s *memoryFanoutStore) fenced(token, id string) (*memoryFanoutRow, error) {
	r := s.row(id)
	if r == nil || r.status != "leased" || r.token != token {
		return nil, application.ErrCommittedEventClaimLost
	}
	return r, nil
}

func (s *memoryFanoutStore) StartCommittedEvent(_ context.Context, token, id string, lease time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.fenced(token, id)
	if err != nil {
		return err
	}
	r.attempts++
	r.lease = lease
	return nil
}

func (s *memoryFanoutStore) CompleteCommittedEvent(_ context.Context, token, id string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.fenced(token, id)
	if err != nil {
		return err
	}
	r.status, r.token = "done", ""
	return nil
}

func (s *memoryFanoutStore) FailCommittedEvent(_ context.Context, f application.CommittedEventFailure) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.fenced(f.Token, f.EventID)
	if err != nil {
		return err
	}
	r.token, r.lastError = "", f.Error
	if f.Dead {
		r.status = "dead"
	} else {
		r.status, r.next = "pending", f.NextAttemptAt
	}
	return nil
}

func (s *memoryFanoutStore) ReleaseCommittedEvents(_ context.Context, token, session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.token == token && r.event.Session == session && r.status == "leased" {
			r.status, r.token = "pending", ""
		}
	}
	return nil
}

func (s *memoryFanoutStore) CommittedEventBacklog(context.Context) (application.CommittedEventBacklog, error) {
	return application.CommittedEventBacklog{}, nil
}

func (s *memoryFanoutStore) status(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.row(id).status
}

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestCommittedEventWorker(store application.CommittedEventWorkStore, dispatcher committedEventFanout, clock *testClock) *CommittedEventWorker {
	worker, err := NewCommittedEventWorker(store, dispatcher, CommittedEventWorkerConfig{
		Owner: "api-test", Lease: time.Minute, Sessions: 8, PerSession: 8, Poll: time.Second,
		MaxAttempts: 3, BackoffBase: time.Second, BackoffCap: time.Minute, Now: clock.now,
	})
	if err != nil {
		panic(err)
	}
	return worker
}

// consumerFanout adapts a plain consumer to the worker's dispatch port.
type consumerFanout struct {
	application.CommittedEventConsumer
}

func (c consumerFanout) DispatchCommittedEvent(ctx context.Context, event domain.Event, _ string) error {
	return c.ConsumeCommittedEvent(ctx, event)
}

func committedEvent() domain.Event {
	return domain.Event{ID: "evt_01", Type: domain.EventMessage, Organization: "org_01", Session: "ses_01"}
}

type selectiveCommittedConsumer struct {
	seen []string
	errs map[string]error
}

func (c *selectiveCommittedConsumer) ConsumeCommittedEvent(_ context.Context, event domain.Event) error {
	c.seen = append(c.seen, event.ID)
	return c.errs[event.ID]
}

func TestCommittedEventWorkerFailureDoesNotBlockUnrelatedSession(t *testing.T) {
	clock := &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := newMemoryFanoutStore(clock.now,
		domain.Event{ID: "bad", Session: "session_a"},
		domain.Event{ID: "dependent", Session: "session_a"},
		domain.Event{ID: "independent", Session: "session_b"},
	)
	cause := errors.New("redis unavailable")
	consumer := &selectiveCommittedConsumer{errs: map[string]error{"bad": cause}}
	worker := newTestCommittedEventWorker(store, consumerFanout{consumer}, clock)
	finished, err := worker.RunOnce(context.Background())
	if finished != 1 || !errors.Is(err, cause) {
		t.Fatalf("finished=%d err=%v", finished, err)
	}
	if store.status("independent") != "done" || store.status("bad") != "pending" || store.status("dependent") != "pending" {
		t.Fatalf("statuses bad=%s dependent=%s independent=%s",
			store.status("bad"), store.status("dependent"), store.status("independent"))
	}
	if len(consumer.seen) != 2 || consumer.seen[1] != "independent" {
		t.Fatalf("dependent work overtook failed event: seen=%v", consumer.seen)
	}
	// The failed head is backed off; its session waits until it is due.
	consumer.seen = nil
	if _, err := worker.RunOnce(context.Background()); err != nil || len(consumer.seen) != 0 {
		t.Fatalf("backed-off session was claimed early: seen=%v err=%v", consumer.seen, err)
	}
}

func TestCommittedEventWorkerPermanentFailureDeadLettersAndUnblocksSession(t *testing.T) {
	clock := &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := newMemoryFanoutStore(clock.now,
		domain.Event{ID: "poison", Session: "session_a"},
		domain.Event{ID: "next", Session: "session_a"},
	)
	consumer := &selectiveCommittedConsumer{errs: map[string]error{
		"poison": application.PermanentEventError(errors.New("undecodable payload")),
	}}
	worker := newTestCommittedEventWorker(store, consumerFanout{consumer}, clock)
	finished, err := worker.RunOnce(context.Background())
	if err != nil || finished != 2 {
		t.Fatalf("finished=%d err=%v", finished, err)
	}
	if store.status("poison") != "dead" || store.status("next") != "done" {
		t.Fatalf("poison=%s next=%s", store.status("poison"), store.status("next"))
	}
	if store.row("poison").lastError == "" {
		t.Fatal("dead-lettered event lost its error")
	}
}

func TestCommittedEventWorkerDeadLettersAfterMaxAttempts(t *testing.T) {
	clock := &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := newMemoryFanoutStore(clock.now,
		domain.Event{ID: "flaky", Session: "session_a"},
		domain.Event{ID: "after", Session: "session_a"},
	)
	consumer := &selectiveCommittedConsumer{errs: map[string]error{"flaky": errors.New("still failing")}}
	worker := newTestCommittedEventWorker(store, consumerFanout{consumer}, clock)
	for attempt := 1; attempt <= 3; attempt++ {
		_, _ = worker.RunOnce(context.Background())
		clock.advance(time.Hour)
	}
	if store.status("flaky") != "dead" || store.status("after") != "done" {
		t.Fatalf("flaky=%s after=%s", store.status("flaky"), store.status("after"))
	}
	if got := store.row("flaky").attempts; got != 3 {
		t.Fatalf("attempts=%d, want 3", got)
	}
}

func TestCommittedEventWorkerCountsCrashedAttempts(t *testing.T) {
	clock := &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := newMemoryFanoutStore(clock.now, domain.Event{ID: "crashy", Session: "session_a"})
	// Three claims that start the event and then "crash" (never record an
	// outcome) use up every attempt; the next claim dead-letters it.
	for i := 0; i < 3; i++ {
		token := domain.NewULID()
		claimed, _ := store.ClaimCommittedEvents(context.Background(), application.CommittedEventClaim{
			Token: token, ClaimedAt: clock.now(), LeaseUntil: clock.now().Add(time.Minute), MaxSessions: 1, MaxPerSession: 1,
		})
		if len(claimed) != 1 {
			t.Fatalf("round %d: claimed %d", i, len(claimed))
		}
		_ = store.StartCommittedEvent(context.Background(), token, "crashy", clock.now().Add(time.Minute))
		clock.advance(2 * time.Minute)
	}
	consumer := &selectiveCommittedConsumer{}
	worker := newTestCommittedEventWorker(store, consumerFanout{consumer}, clock)
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.status("crashy") != "dead" || len(consumer.seen) != 0 {
		t.Fatalf("status=%s dispatched=%v", store.status("crashy"), consumer.seen)
	}
}

func TestCommittedEventWorkerFailureReplaysAfterRestart(t *testing.T) {
	clock := &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	publisher := &recordingCommittedPublisher{err: errors.New("redis unavailable")}
	store := newMemoryFanoutStore(clock.now, committedEvent())
	first := newTestCommittedEventWorker(store, NewCommittedEventDispatcher(nil, publisher, nil), clock)
	if finished, err := first.RunOnce(context.Background()); err == nil || finished != 0 {
		t.Fatalf("finished, err = %d, %v", finished, err)
	}
	if store.status("evt_01") != "pending" {
		t.Fatal("failed event was completed")
	}

	publisher.mu.Lock()
	publisher.err = nil
	publisher.mu.Unlock()
	clock.advance(time.Minute)
	// A new worker models process restart; the durable store exposes the
	// pending event again once its backoff is due.
	restarted := newTestCommittedEventWorker(store, NewCommittedEventDispatcher(nil, publisher, nil), clock)
	if finished, err := restarted.RunOnce(context.Background()); err != nil || finished != 1 {
		t.Fatalf("finished, err = %d, %v", finished, err)
	}
	if store.status("evt_01") != "done" || publisher.count() != 2 {
		t.Fatalf("status=%s publish calls=%d", store.status("evt_01"), publisher.count())
	}
}

func TestCommittedEventDispatcherSkipsProjectionsForAPIEvents(t *testing.T) {
	projection := &recordingCommittedConsumer{}
	publisher := &recordingCommittedPublisher{}
	webhooks := &recordingCommittedWebhooks{}
	dispatcher := NewCommittedEventDispatcher([]application.CommittedEventConsumer{projection}, publisher, webhooks)
	if err := dispatcher.DispatchCommittedEvent(context.Background(), committedEvent(), application.CommittedEventSourceAPI); err != nil {
		t.Fatal(err)
	}
	if projection.count() != 0 || publisher.count() != 1 || len(webhooks.calls) != 1 {
		t.Fatalf("projection=%d publisher=%d webhooks=%d", projection.count(), publisher.count(), len(webhooks.calls))
	}
}

func TestCommittedEventDispatcherStopsFanoutForHandledEvent(t *testing.T) {
	clock := &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	login := &handledCommittedConsumer{handled: true}
	projection := &recordingCommittedConsumer{}
	publisher := &recordingCommittedPublisher{}
	webhooks := &recordingCommittedWebhooks{}
	store := newMemoryFanoutStore(clock.now, committedEvent())
	worker := newTestCommittedEventWorker(
		store,
		NewCommittedEventDispatcher(
			[]application.CommittedEventConsumer{
				NewCommittedEventConsumers(login, projection),
			},
			publisher,
			webhooks,
		),
		clock,
	)

	finished, err := worker.RunOnce(context.Background())
	if err != nil || finished != 1 {
		t.Fatalf("finished, err = %d, %v", finished, err)
	}
	if projection.count() != 0 || publisher.count() != 0 || len(webhooks.calls) != 0 {
		t.Fatalf("handled event leaked: projection=%d publisher=%d webhooks=%d", projection.count(), publisher.count(), len(webhooks.calls))
	}
}
