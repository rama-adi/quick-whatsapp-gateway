package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// This file holds the async-worker adapters the asynq queue dispatches to
// (queue.OutboxProcessor / queue.RetentionPruner). They are wired in cmd/server
// onto the queue.Handlers struct.

// OutboxWorker drains a persisted outbox row to WhatsApp: load it, re-dispatch
// the stored request via the outbound.Sender, then record the outcome. It
// satisfies queue.OutboxProcessor.
type OutboxWorker struct {
	outbox *store.OutboxRepo
	sender *outbound.Sender
	log    *slog.Logger
}

// NewOutboxWorker constructs an OutboxWorker.
func NewOutboxWorker(outbox *store.OutboxRepo, sender *outbound.Sender, log *slog.Logger) *OutboxWorker {
	if log == nil {
		log = slog.Default()
	}
	return &OutboxWorker{outbox: outbox, sender: sender, log: log}
}

// ProcessOutbox loads outboxID, dispatches the stored SendRequest, and records
// the result. Already-terminal rows are skipped (idempotent re-delivery).
func (w *OutboxWorker) ProcessOutbox(ctx context.Context, outboxID string) error {
	entry, err := w.outbox.Get(ctx, outboxID)
	if err != nil {
		return fmt.Errorf("load outbox %s: %w", outboxID, err)
	}
	if entry.Status == domain.OutboxSent {
		return nil
	}
	var req domain.SendRequest
	if err := json.Unmarshal(entry.Payload, &req); err != nil {
		errMsg := "malformed outbox payload"
		_ = w.outbox.UpdateStatus(ctx, outboxID, domain.OutboxFailed, nil, &errMsg, domain.NowMs())
		return fmt.Errorf("unmarshal outbox %s payload: %w", outboxID, err)
	}
	// Carry the session id so the session-routing WAClient resolves the right
	// per-session whatsmeow client when the worker drains the row.
	waID, _, err := w.sender.Dispatch(outbound.WithSessionID(ctx, entry.SessionID), req)
	if err != nil {
		errMsg := err.Error()
		_ = w.outbox.UpdateStatus(ctx, outboxID, domain.OutboxFailed, nil, &errMsg, domain.NowMs())
		return fmt.Errorf("dispatch outbox %s: %w", outboxID, err)
	}
	if err := w.outbox.UpdateStatus(ctx, outboxID, domain.OutboxSent, &waID, nil, domain.NowMs()); err != nil {
		return fmt.Errorf("update outbox %s status: %w", outboxID, err)
	}
	return nil
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
