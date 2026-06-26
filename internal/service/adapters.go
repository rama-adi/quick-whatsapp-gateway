package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa"
)

// This file holds the small impedance-matching adapters the composition root
// needs to plug the concrete store/stream/queue types into the consumer
// interfaces declared by internal/wa, internal/webhooks and internal/queue.
// They live here (a non-main package) so they are unit-testable and so cmd/server
// stays a thin wiring shim.

// ---------------------------------------------------------------------------
// wa.SessionRepo: the store.SessionRepo speaks value types + a 4-arg
// UpdateStatus; the manager's consumer interface speaks pointer types + a 3-arg
// UpdateStatus (stamping last_connected_at on WORKING). This adapter bridges the
// two without changing either side.
// ---------------------------------------------------------------------------

// ManagerSessionRepo adapts *store.SessionRepo to wa.SessionRepo.
type ManagerSessionRepo struct {
	repo  *store.SessionRepo
	clock func() int64
}

// NewManagerSessionRepo wraps a store.SessionRepo for the wa.Manager. clock may
// be nil (domain.NowMs is used).
func NewManagerSessionRepo(repo *store.SessionRepo, clock func() int64) *ManagerSessionRepo {
	if clock == nil {
		clock = domain.NowMs
	}
	return &ManagerSessionRepo{repo: repo, clock: clock}
}

var _ wa.SessionRepo = (*ManagerSessionRepo)(nil)

func (a *ManagerSessionRepo) Get(ctx context.Context, id string) (*domain.WASession, error) {
	s, err := a.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (a *ManagerSessionRepo) GetByJID(ctx context.Context, jid string) (*domain.WASession, error) {
	s, err := a.repo.GetByJID(ctx, jid)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (a *ManagerSessionRepo) ListByOrg(ctx context.Context, organizationID string) ([]*domain.WASession, error) {
	rows, err := a.repo.ListByOrg(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.WASession, len(rows))
	for i := range rows {
		s := rows[i]
		out[i] = &s
	}
	return out, nil
}

func (a *ManagerSessionRepo) Create(ctx context.Context, s *domain.WASession) error {
	return a.repo.Create(ctx, *s)
}

func (a *ManagerSessionRepo) Update(ctx context.Context, s *domain.WASession) error {
	s.UpdatedAt = a.clock()
	return a.repo.Update(ctx, *s)
}

func (a *ManagerSessionRepo) UpdateStatus(ctx context.Context, id string, status domain.SessionStatus) error {
	now := a.clock()
	if err := a.repo.UpdateStatus(ctx, id, status, now); err != nil {
		return err
	}
	// Stamp last_connected_at when a session reaches WORKING. Best-effort: load,
	// set, write through the full Update. A failure here is logged by the caller.
	if status == domain.SessionWorking {
		s, err := a.repo.Get(ctx, id)
		if err != nil {
			return nil //nolint:nilerr // status already persisted; stamping is best-effort
		}
		s.LastConnectedAt = &now
		s.UpdatedAt = now
		_ = a.repo.Update(ctx, s)
	}
	return nil
}

// ---------------------------------------------------------------------------
// wa.EventSink: stream.Publisher.Publish returns an error; the manager's sink
// is fire-and-forget (no return). This adapter logs publish failures.
// ---------------------------------------------------------------------------

// publisher is the slice of *stream.Publisher this adapter needs.
type publisher interface {
	Publish(ctx context.Context, e domain.Event) error
}

// EventSinkAdapter adapts a *stream.Publisher (Publish returning error) to the
// fire-and-forget wa.EventSink the manager expects.
type EventSinkAdapter struct {
	pub publisher
	log *slog.Logger
}

// NewEventSinkAdapter wraps a publisher for the wa.Manager. log may be nil.
func NewEventSinkAdapter(pub publisher, log *slog.Logger) *EventSinkAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &EventSinkAdapter{pub: pub, log: log}
}

var _ wa.EventSink = (*EventSinkAdapter)(nil)

func (a *EventSinkAdapter) Publish(ctx context.Context, evt domain.Event) {
	if err := a.pub.Publish(ctx, evt); err != nil {
		a.log.WarnContext(ctx, "event publish failed", "event_id", evt.ID, "type", evt.Type, "err", err)
	}
}

// ---------------------------------------------------------------------------
// wa.InboundHandler: the full inbound pipeline (normalize -> capture ->
// persist -> auto-read -> fan-out) is wired in the next stage, which needs a
// Repos facade adapter, a per-session WAClient for read receipts, and a
// Normalizer adapter mapping events.PersistResult -> inbound.NormalizedMessage.
// Until then the manager is given this forwarding handler so live clients still
// boot; it is a no-op beyond logging. The session.status/auth.qr/auth.code
// events the manager emits itself still flow through the real EventSink, so the
// live event stream works for the lifecycle events today.
// ---------------------------------------------------------------------------

// InboundLogHandler is the placeholder wa.InboundHandler used until the full
// pipeline is wired. STUB (next stage): replace with the internal/wa/inbound
// Pipeline behind a Repos/Normalizer/WAClient adapter set.
type InboundLogHandler struct {
	log *slog.Logger
}

// NewInboundLogHandler constructs an InboundLogHandler. log may be nil.
func NewInboundLogHandler(log *slog.Logger) *InboundLogHandler {
	if log == nil {
		log = slog.Default()
	}
	return &InboundLogHandler{log: log}
}

var _ wa.InboundHandler = (*InboundLogHandler)(nil)

func (h *InboundLogHandler) Handle(ctx context.Context, sessionID, organizationID string, isAdmin bool, evt any) {
	h.log.DebugContext(ctx, "inbound event (pipeline not yet wired)",
		"session", sessionID, "organization", organizationID, "type", typeName(evt))
}

func typeName(v any) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", v)
}
