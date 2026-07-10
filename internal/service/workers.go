package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// This file holds the async-worker adapters the asynq queue dispatches to
// (queue.OutboxProcessor / queue.RetentionPruner). They are wired in cmd/server
// onto the queue.Handlers struct.

const (
	// A dispatch is bounded below the durable claim lease. The one-minute gap
	// keeps a healthy owner from being reclaimed while it records its outcome.
	outboxDispatchTimeout = 4 * time.Minute
	outboxClaimLease      = 5 * time.Minute
	outboxBookkeepingTTL  = 5 * time.Second
)

// OutboxWorker drains a persisted outbox row to WhatsApp. A cancellable local
// keyed gate reduces duplicate contention, then the repository atomically claims
// queued/failed -> sending; only that CAS winner may decode and dispatch. Success
// records the WhatsApp id, while malformed payloads and send failures become
// failed so Asynq can claim them again according to its retry policy. Delivery is
// necessarily at-least-once across a crash after WhatsApp accepts the message but
// before MarkSent commits: the stale lease recovers liveness at the cost of a
// possible duplicate because WhatsApp exposes no idempotency key for this send.
type OutboxWorker struct {
	outbox outboxWorkerRepo
	sender outboxDispatcher
	log    *slog.Logger
	mu     sync.Mutex
	gates  map[string]*outboxGate
}

type outboxWorkerRepo interface {
	ClaimByID(context.Context, string, int64, int64) (bool, error)
	Get(context.Context, string) (domain.OutboxEntry, error)
	UpdateStatus(context.Context, string, domain.OutboxStatus, *string, *string, int64) error
}

type outboxDispatcher interface {
	Dispatch(context.Context, domain.SendRequest) (string, int64, error)
}

type outboxGate struct {
	token chan struct{}
	refs  int
}

// NewOutboxWorker constructs an OutboxWorker.
func NewOutboxWorker(outbox outboxWorkerRepo, sender outboxDispatcher, log *slog.Logger) *OutboxWorker {
	if log == nil {
		log = slog.Default()
	}
	return &OutboxWorker{outbox: outbox, sender: sender, log: log, gates: make(map[string]*outboxGate)}
}

// ProcessOutbox claims outboxID, dispatches its stored SendRequest, and records
// the result. A lost claim is a successful no-op: another worker owns sending or
// the row is terminal. Claim/database and dispatch failures are returned for
// Asynq retry; status-update failures are returned because ownership would
// otherwise be left uncertain.
func (w *OutboxWorker) ProcessOutbox(ctx context.Context, outboxID string) error {
	release, err := w.acquire(ctx, outboxID)
	if err != nil {
		return err
	}
	defer release()

	now := domain.NowMs()
	claimed, err := w.outbox.ClaimByID(ctx, outboxID, now, now-outboxClaimLease.Milliseconds())
	if err != nil {
		return fmt.Errorf("claim outbox %s: %w", outboxID, err)
	}
	if !claimed {
		// Missing, already sending, or terminal. Another worker that won the CAS
		// owns any in-flight dispatch, so duplicate tasks are normal no-ops.
		return nil
	}

	entry, err := w.outbox.Get(ctx, outboxID)
	if err != nil {
		// The claim is now sending and will be recoverable after its lease. Return
		// the read failure so Asynq retries; do not dispatch without durable payload.
		return fmt.Errorf("load outbox %s: %w", outboxID, err)
	}
	if entry.Status != domain.OutboxSending {
		return fmt.Errorf("load claimed outbox %s: expected status %s, got %s", outboxID, domain.OutboxSending, entry.Status)
	}
	var req domain.SendRequest
	if err := json.Unmarshal(entry.Payload, &req); err != nil {
		errMsg := "malformed outbox payload"
		parseErr := fmt.Errorf("unmarshal outbox %s payload: %w", outboxID, err)
		if updateErr := w.updateStatus(ctx, outboxID, domain.OutboxFailed, nil, &errMsg); updateErr != nil {
			return errors.Join(parseErr, fmt.Errorf("record malformed outbox %s: %w", outboxID, updateErr))
		}
		return parseErr
	}
	// Carry the session id so the session-routing WAClient resolves the right
	// per-session whatsmeow client when the worker drains the row.
	dispatchCtx, cancel := context.WithTimeout(outbound.WithSessionID(ctx, entry.SessionID), outboxDispatchTimeout)
	defer cancel()
	waID, _, err := w.sender.Dispatch(dispatchCtx, req)
	if err != nil {
		errMsg := err.Error()
		dispatchErr := fmt.Errorf("dispatch outbox %s: %w", outboxID, err)
		if updateErr := w.updateStatus(ctx, outboxID, domain.OutboxFailed, nil, &errMsg); updateErr != nil {
			return errors.Join(dispatchErr, fmt.Errorf("record failed outbox %s: %w", outboxID, updateErr))
		}
		return dispatchErr
	}
	if err := w.updateStatus(ctx, outboxID, domain.OutboxSent, &waID, nil); err != nil {
		return fmt.Errorf("update outbox %s status: %w", outboxID, err)
	}
	return nil
}

// updateStatus gives an owned attempt a short cleanup window independent of
// task cancellation. Without detaching, a dispatch timeout would make the
// immediate failed/sent write inherit an already-done context and force the row
// to wait for stale-lease recovery even though this process knows the outcome.
func (w *OutboxWorker) updateStatus(ctx context.Context, id string, status domain.OutboxStatus, waID, message *string) error {
	bookkeepingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), outboxBookkeepingTTL)
	defer cancel()
	return w.outbox.UpdateStatus(bookkeepingCtx, id, status, waID, message, domain.NowMs())
}

// acquire serializes attempts for one outbox id inside a worker process, avoiding
// needless database contention between local duplicates. Durable ownership is
// still established by ClaimByID after admission, so workers in other replicas
// cannot dispatch the same row. A waiter observes context cancellation without
// taking either the local token or the database claim.
func (w *OutboxWorker) acquire(ctx context.Context, id string) (func(), error) {
	w.mu.Lock()
	gate := w.gates[id]
	if gate == nil {
		gate = &outboxGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		w.gates[id] = gate
	}
	gate.refs++
	w.mu.Unlock()

	select {
	case <-ctx.Done():
		w.releaseRef(id, gate)
		return nil, ctx.Err()
	case <-gate.token:
		return func() {
			gate.token <- struct{}{}
			w.releaseRef(id, gate)
		}, nil
	}
}

func (w *OutboxWorker) releaseRef(id string, gate *outboxGate) {
	w.mu.Lock()
	defer w.mu.Unlock()
	gate.refs--
	if gate.refs == 0 && w.gates[id] == gate {
		delete(w.gates, id)
	}
}

// RetentionWorker prunes old rows past the retention cutoff. The concrete prune
// queries land in the next stage; for now it is a no-op so the queue handler is
// registered and the daily task succeeds.
type RetentionWorker struct {
	store *store.Store
	log   *slog.Logger
}

// NewRetentionWorker constructs a RetentionWorker.
func NewRetentionWorker(s *store.Store, log *slog.Logger) *RetentionWorker {
	if log == nil {
		log = slog.Default()
	}
	return &RetentionWorker{store: s, log: log}
}

// Prune removes rows older than cutoffMs. STUB: the prune SQL is implemented in
// the next stage; for now it logs and succeeds.
func (w *RetentionWorker) Prune(ctx context.Context, cutoffMs int64) error {
	w.log.InfoContext(ctx, "retention prune (no-op stub)", "cutoffMs", cutoffMs)
	return nil
}
