package main

import (
	"context"
	"log/slog"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/config"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/desiredstate"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/historysync"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/journal"
)

// newHistoryWorker wires history import: the journal SQLite holds the inbox,
// the manager supplies connected clients, and batches enter the same journal
// handoff as live events (subject to the same live-session ownership rule).
func newHistoryWorker(
	eventJournal *journal.Journal,
	sessions historysync.Sessions,
	sink historysync.Sink,
	reconciler *desiredstate.Reconciler,
	cfg *config.GatewayConfig,
	log *slog.Logger,
) *historysync.Worker {
	return historysync.NewWorker(eventJournal, sessions, sink, historysync.Config{
		Concurrency: cfg.HistoryConcurrency,
		FullSync:    cfg.HistoryFullSync,
		Owns: func(organizationID, sessionID string) bool {
			epoch, ok := reconciler.CurrentEpoch(sessionID)
			return ok && reconciler.OwnsSession(organizationID, sessionID, epoch)
		},
		// Imports yield to live traffic: they pause as soon as the journal
		// leaves the healthy band and resume once the API has drained it.
		Paused: func(ctx context.Context) bool {
			metrics, err := eventJournal.Metrics(ctx)
			return err != nil || metrics.State != journal.CapacityHealthy
		},
	}, log)
}
