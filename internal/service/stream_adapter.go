package service

import (
	"context"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/stream"
)

// EventLogReaderAdapter adapts *store.EventLogRepo to stream.EventLogReader. The
// stream resumes from an opaque event_id (ULID) via ?since=, while the store
// pages on its monotonic numeric cursor; this adapter resolves the event_id to
// that cursor before listing.
type EventLogReaderAdapter struct {
	repo *store.EventLogRepo
}

// NewEventLogReaderAdapter wraps a store.EventLogRepo for the stream handler.
func NewEventLogReaderAdapter(repo *store.EventLogRepo) *EventLogReaderAdapter {
	return &EventLogReaderAdapter{repo: repo}
}

var _ stream.EventLogReader = (*EventLogReaderAdapter)(nil)

// ListSince resolves afterEventID to the monotonic cursor and returns the next
// page of entries for the organization/session. An empty/unknown afterEventID replays
// from the start.
func (a *EventLogReaderAdapter) ListSince(ctx context.Context, organization, session, afterEventID string, limit int) ([]domain.EventLogEntry, error) {
	var afterID uint64
	if afterEventID != "" {
		entry, err := a.repo.GetByEventID(ctx, afterEventID)
		if err == nil {
			afterID = entry.ID
		}
		// Unknown event id -> replay from start (afterID stays 0).
	}
	return a.repo.ListSince(ctx, organization, session, afterID, limit)
}
