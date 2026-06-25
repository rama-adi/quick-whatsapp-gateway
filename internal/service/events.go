package service

import (
	"context"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// EventsService is the REST-side view over the persisted event log. The live
// NDJSON stream is served by internal/stream's Handler directly; this service
// backs any future event-log REST reads and is the home for event-log queries.
type EventsService struct {
	log    *store.EventLogRepo
	logger *slog.Logger
}

// NewEventsService constructs an EventsService.
func NewEventsService(eventLog *store.EventLogRepo, logger *slog.Logger) *EventsService {
	if logger == nil {
		logger = slog.Default()
	}
	return &EventsService{log: eventLog, logger: logger}
}

// ListSince returns event-log entries after the given monotonic cursor.
func (s *EventsService) ListSince(ctx context.Context, tenantID, sessionID string, afterID uint64, limit int) ([]domain.EventLogEntry, error) {
	return s.log.ListSince(ctx, tenantID, sessionID, afterID, limit)
}
