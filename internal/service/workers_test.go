package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

type workerOutboxRepo struct {
	mu           sync.Mutex
	entry        domain.OutboxEntry
	updateCtxErr error
}

func (r *workerOutboxRepo) ClaimByID(_ context.Context, _ string, updatedAt, staleBefore int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	claimable := r.entry.Status == domain.OutboxQueued || r.entry.Status == domain.OutboxFailed ||
		(r.entry.Status == domain.OutboxSending && r.entry.UpdatedAt <= staleBefore)
	if !claimable {
		return false, nil
	}
	r.entry.Status = domain.OutboxSending
	r.entry.Attempts++
	r.entry.UpdatedAt = updatedAt
	return true, nil
}

func (r *workerOutboxRepo) Get(context.Context, string) (domain.OutboxEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entry, nil
}

func (r *workerOutboxRepo) UpdateStatus(ctx context.Context, _ string, status domain.OutboxStatus, waID, message *string, updatedAt int64) error {
	return r.updateStatus(ctx, status, waID, message, updatedAt)
}

func (r *workerOutboxRepo) updateStatus(ctx context.Context, status domain.OutboxStatus, waID, message *string, updatedAt int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateCtxErr = ctx.Err()
	r.entry.Status = status
	r.entry.WAMessageID = waID
	r.entry.Error = message
	r.entry.UpdatedAt = updatedAt
	return nil
}

type blockingOutboxDispatcher struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

type cancelAwareOutboxDispatcher struct{ started chan struct{} }

func (d cancelAwareOutboxDispatcher) Dispatch(ctx context.Context, _ domain.SendRequest) (string, int64, error) {
	close(d.started)
	<-ctx.Done()
	return "", 0, ctx.Err()
}

func (d *blockingOutboxDispatcher) Dispatch(context.Context, domain.SendRequest) (string, int64, error) {
	d.mu.Lock()
	d.calls++
	first := d.calls == 1
	d.mu.Unlock()
	if first {
		close(d.started)
	}
	<-d.release
	return "wa_1", 1234, nil
}

func (d *blockingOutboxDispatcher) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// TestOutboxWorkerSerializesDuplicateAttempts starts two workers for the same queued row while the first
// WhatsApp dispatch is deliberately blocked. The second call waits for the local gate, then loses the durable
// queued/failed-to-sending compare-and-set after the first marks the row sent, so it returns without another
// dispatch. This establishes the per-outbox ownership handoff used by local retries and worker replicas.
func TestOutboxWorkerSerializesDuplicateAttempts(t *testing.T) {
	payload, err := json.Marshal(domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"})
	require.NoError(t, err)
	repo := &workerOutboxRepo{entry: domain.OutboxEntry{
		ID: "out_1", SessionID: "sess_1", Status: domain.OutboxQueued, Payload: payload,
	}}
	dispatcher := &blockingOutboxDispatcher{started: make(chan struct{}), release: make(chan struct{})}
	worker := NewOutboxWorker(repo, dispatcher, nil)

	errs := make(chan error, 2)
	go func() { errs <- worker.ProcessOutbox(context.Background(), "out_1") }()
	<-dispatcher.started
	go func() { errs <- worker.ProcessOutbox(context.Background(), "out_1") }()
	close(dispatcher.release)

	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.Equal(t, 1, dispatcher.callCount())
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, domain.OutboxSent, repo.entry.Status)
	require.NotNil(t, repo.entry.WAMessageID)
	require.Equal(t, "wa_1", *repo.entry.WAMessageID)
}

// TestOutboxWorkerCASAllowsOneReplicaOwner uses two independent worker instances, so their in-memory keyed
// gates cannot coordinate, against one repository row. Concurrent ProcessOutbox calls must produce exactly
// one queued-to-sending claim and one WhatsApp dispatch; the CAS loser treats the duplicate task as a normal
// no-op. This is the deterministic regression for duplicate task delivery across gateway replicas.
func TestOutboxWorkerCASAllowsOneReplicaOwner(t *testing.T) {
	payload, err := json.Marshal(domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"})
	require.NoError(t, err)
	repo := &workerOutboxRepo{entry: domain.OutboxEntry{
		ID: "out_1", SessionID: "sess_1", Status: domain.OutboxQueued, Payload: payload,
	}}
	dispatcher := &blockingOutboxDispatcher{started: make(chan struct{}), release: make(chan struct{})}
	workerA := NewOutboxWorker(repo, dispatcher, nil)
	workerB := NewOutboxWorker(repo, dispatcher, nil)
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() { <-start; errs <- workerA.ProcessOutbox(context.Background(), "out_1") }()
	go func() { <-start; errs <- workerB.ProcessOutbox(context.Background(), "out_1") }()
	close(start)
	<-dispatcher.started
	close(dispatcher.release)

	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.Equal(t, 1, dispatcher.callCount())
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, 1, repo.entry.Attempts)
	require.Equal(t, domain.OutboxSent, repo.entry.Status)
}

// TestOutboxWorkerDoesNotStealFreshSendingClaim presents a row whose sending timestamp is newer than the
// five-minute lease cutoff, modeling an active worker in another replica. ClaimByID must reject ownership,
// ProcessOutbox returns a normal no-op, and the WhatsApp dispatcher is never called. This proves retry tasks
// cannot steal a healthy in-flight send merely because they run concurrently.
func TestOutboxWorkerDoesNotStealFreshSendingClaim(t *testing.T) {
	payload, err := json.Marshal(domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"})
	require.NoError(t, err)
	repo := &workerOutboxRepo{entry: domain.OutboxEntry{
		ID: "out_1", SessionID: "sess_1", Status: domain.OutboxSending, Payload: payload,
		Attempts: 1, UpdatedAt: domain.NowMs(),
	}}
	dispatcher := &blockingOutboxDispatcher{started: make(chan struct{}), release: make(chan struct{})}
	worker := NewOutboxWorker(repo, dispatcher, nil)

	require.NoError(t, worker.ProcessOutbox(context.Background(), "out_1"))
	require.Equal(t, 0, dispatcher.callCount())
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, 1, repo.entry.Attempts)
	require.Equal(t, domain.OutboxSending, repo.entry.Status)
}

// TestOutboxWorkerReclaimsExpiredSendingClaim gives the worker a sending row older than the lease, modeling
// a process crash after durable claim but before status bookkeeping. The stale row must be reclaimed, its
// attempt count incremented, dispatched once, and marked sent. This is the recovery path that prevents a
// crashed owner from stranding asynchronous messages forever.
func TestOutboxWorkerReclaimsExpiredSendingClaim(t *testing.T) {
	payload, err := json.Marshal(domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"})
	require.NoError(t, err)
	repo := &workerOutboxRepo{entry: domain.OutboxEntry{
		ID: "out_1", SessionID: "sess_1", Status: domain.OutboxSending, Payload: payload,
		Attempts: 1, UpdatedAt: domain.NowMs() - outboxClaimLease.Milliseconds() - 1,
	}}
	release := make(chan struct{})
	close(release)
	dispatcher := &blockingOutboxDispatcher{started: make(chan struct{}), release: release}
	worker := NewOutboxWorker(repo, dispatcher, nil)

	require.NoError(t, worker.ProcessOutbox(context.Background(), "out_1"))
	require.Equal(t, 1, dispatcher.callCount())
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, 2, repo.entry.Attempts)
	require.Equal(t, domain.OutboxSent, repo.entry.Status)
}

// TestOutboxWorkerWaiterCancellationDoesNotTakeOwnership blocks one outbox attempt, then starts a duplicate
// attempt with an already-cancelled context. The waiter must return context.Canceled without dispatching or
// disturbing the gate held by the first attempt; after release, the owner completes normally. This guards the
// cancellation branch of the keyed gate against token leaks and accidental concurrent ownership.
func TestOutboxWorkerWaiterCancellationDoesNotTakeOwnership(t *testing.T) {
	payload, err := json.Marshal(domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"})
	require.NoError(t, err)
	repo := &workerOutboxRepo{entry: domain.OutboxEntry{
		ID: "out_1", SessionID: "sess_1", Status: domain.OutboxQueued, Payload: payload,
	}}
	dispatcher := &blockingOutboxDispatcher{started: make(chan struct{}), release: make(chan struct{})}
	worker := NewOutboxWorker(repo, dispatcher, nil)

	ownerDone := make(chan error, 1)
	go func() { ownerDone <- worker.ProcessOutbox(context.Background(), "out_1") }()
	<-dispatcher.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	require.ErrorIs(t, worker.ProcessOutbox(ctx, "out_1"), context.Canceled)
	require.Less(t, time.Since(start), time.Second)
	require.Equal(t, 1, dispatcher.callCount())
	close(dispatcher.release)
	require.NoError(t, <-ownerDone)
}

// TestOutboxWorkerCancellationStillRecordsFailure cancels the task context after the worker has claimed the
// row and entered dispatch. Dispatch observes cancellation, but failed-status bookkeeping must run under a
// fresh bounded context, leaving the row immediately retryable instead of sending until the lease expires.
// ProcessOutbox still returns the dispatch cancellation so Asynq applies its retry policy.
func TestOutboxWorkerCancellationStillRecordsFailure(t *testing.T) {
	payload, err := json.Marshal(domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"})
	require.NoError(t, err)
	repo := &workerOutboxRepo{entry: domain.OutboxEntry{
		ID: "out_1", SessionID: "sess_1", Status: domain.OutboxQueued, Payload: payload,
	}}
	started := make(chan struct{})
	worker := NewOutboxWorker(repo, cancelAwareOutboxDispatcher{started: started}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.ProcessOutbox(ctx, "out_1") }()
	<-started
	cancel()

	require.ErrorIs(t, <-done, context.Canceled)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, domain.OutboxFailed, repo.entry.Status)
	require.NoError(t, repo.updateCtxErr)
	require.NotNil(t, repo.entry.Error)
	require.Contains(t, *repo.entry.Error, context.Canceled.Error())
}

// TestRetentionWorkerPrune delegates one retention task to the store, preserving
// its safe deletion order and surfacing no error after every bounded phase
// succeeds. The repository has exhaustive batching and reference-preservation
// tests; this pins the queue consumer wiring.
func TestRetentionWorkerPrune(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	const cutoff = int64(1_000)
	mock.ExpectExec("DELETE FROM webhook_deliveries").
		WithArgs(cutoff, "delivered", "dead", int32(1000)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("DELETE FROM messages").
		WithArgs(cutoff, int32(1000)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("DELETE FROM event_log").
		WithArgs(cutoff, "pending", "failed", int32(1000)).
		WillReturnResult(sqlmock.NewResult(0, 4))

	worker := NewRetentionWorker(store.New(db), nil)
	require.NoError(t, worker.Prune(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}
