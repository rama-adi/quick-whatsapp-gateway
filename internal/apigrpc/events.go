package apigrpc

import (
	"context"
	"log/slog"
	"time"

	publicv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/public/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// EventsReader is the committed-event source apigrpc streams from — the exact
// method set of *store.EventLogRepo (via service.EventsService), plus the
// optional event-id lookup used to resolve since-cursors. The durable,
// commit-gated event log is the source of truth: a row exists only after its
// ingest transaction commits, so tailing it never serves an uncommitted event.
type EventsReader interface {
	ListSince(ctx context.Context, organizationID, sessionID string, afterID uint64, limit int) ([]domain.EventLogEntry, error)
}

// EventIDResolver resolves a public event id ("evt_…") to its log entry.
// *store.EventLogRepo satisfies it; fakes in tests may not.
type EventIDResolver interface {
	GetByEventID(ctx context.Context, eventID string) (domain.EventLogEntry, error)
}

// StreamConfig bounds the events tail.
type StreamConfig struct {
	// PollInterval is how often the log is re-checked for new rows while the
	// stream is open. Defaults to 500ms.
	PollInterval time.Duration
	// PageSize caps each ListSince page. Defaults to 500.
	PageSize int
	Log      *slog.Logger
}

// Events implements publicv1.PublicEventsService over the committed event log.
//
// DESIGN DECISION (Increment 8): this is a durable-log tail, not a Redis Pump
// subscription. stream.Pump is bound to go-redis pub/sub types (its tail loop
// consumes *redis.Message channels and its Sink emits framed JSON), so reuse
// would couple the public gRPC surface to the lossy fan-out transport — events
// published while no subscriber is attached are gone. Polling
// EventLogRepo.ListSince instead keeps the same ordering guarantee as the WS
// pump's ?since= replay (ascending monotonic cursor) with at-least-once
// delivery from the durable log, which is commit-gated post-Increment-5. The
// cost is delivery latency bounded by StreamConfig.PollInterval rather than
// instant pub/sub push.
type Events struct {
	publicv1.UnimplementedPublicEventsServiceServer
	Events EventsReader
	Config StreamConfig
}

// NewEvents builds the events adapter.
func NewEvents(events EventsReader, cfg StreamConfig) *Events {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = 500
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Events{Events: events, Config: cfg}
}

func (e *Events) StreamEvents(req *publicv1.StreamEventsRequest, stream publicv1.PublicEventsService_StreamEventsServer) error {
	ctx := stream.Context()
	org, err := requireOrg(ctx, authz.CapEvents)
	if err != nil {
		return err
	}
	filter := parseEventTypes(req.GetEventTypes())
	sessionID := req.GetSessionId()

	// Resolve the opaque evt_ cursor to the log's monotonic id, mirroring the
	// WebSocket ?since= resume. An unresolvable cursor starts from the oldest
	// retained event for the scope rather than failing the stream.
	var afterID uint64
	if since := req.GetSinceCursor(); since != "" {
		if getter, ok := e.Events.(EventIDResolver); ok {
			if entry, err := getter.GetByEventID(ctx, since); err == nil && entry.OrganizationID == org {
				afterID = entry.ID
			}
		}
	}

	ticker := time.NewTicker(e.Config.PollInterval)
	defer ticker.Stop()
	for {
		page, err := e.Events.ListSince(ctx, org, sessionID, afterID, e.Config.PageSize)
		if err != nil {
			if ctx.Err() != nil {
				return nil // shutdown raced the query; client is gone anyway
			}
			return Status(err)
		}
		for _, entry := range page {
			afterID = entry.ID // advance even past filtered-out types
			if !filter.allows(entry.Type) {
				continue
			}
			if err := stream.Send(eventToProto(entry)); err != nil {
				return err // client cancelled or connection gone
			}
		}
		select {
		case <-ctx.Done():
			// Client cancellation is the normal end of a server stream.
			return nil
		case <-ticker.C:
		}
	}
}

// eventTypeFilter mirrors the stream package's ?events= allow-list semantics:
// empty (or any "*" element) delivers every type, otherwise an exact-name
// allow-list applies.
type eventTypeFilter struct {
	all   bool
	types map[string]struct{}
}

func parseEventTypes(types []string) eventTypeFilter {
	if len(types) == 0 {
		return eventTypeFilter{all: true}
	}
	f := eventTypeFilter{types: make(map[string]struct{}, len(types))}
	for _, t := range types {
		if t == "" || t == "*" {
			return eventTypeFilter{all: true}
		}
		f.types[t] = struct{}{}
	}
	return f
}

func (f eventTypeFilter) allows(t string) bool {
	if f.all {
		return true
	}
	_, ok := f.types[t]
	return ok
}

func eventToProto(entry domain.EventLogEntry) *publicv1.StreamEventsResponse {
	return &publicv1.StreamEventsResponse{
		Schema:          domain.Schema,
		Id:              entry.EventID,
		Event:           entry.Type,
		SessionId:       entry.SessionID,
		OrganizationId:  entry.OrganizationID,
		TimestampUnixMs: entry.CreatedAt,
		PayloadJson:     entry.Payload,
	}
}
