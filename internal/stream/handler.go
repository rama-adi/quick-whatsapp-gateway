package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// OrganizationAccessor extracts the authenticated organization id from the request context.
// Auth is applied by upstream middleware (Phase 3); this handler only reads the
// result. Defining it as a consumer interface keeps the auth package out of our
// imports — Phase 3 supplies a func adapter.
type OrganizationAccessor interface {
	OrganizationFromContext(ctx context.Context) (organizationID string, ok bool)
}

// OrganizationAccessorFunc adapts a plain function to OrganizationAccessor.
type OrganizationAccessorFunc func(ctx context.Context) (string, bool)

func (f OrganizationAccessorFunc) OrganizationFromContext(ctx context.Context) (string, bool) {
	return f(ctx)
}

// resumeReplayLimit caps how many event-log entries a single ?since= resume
// replays before switching to the live tail, bounding memory/latency on a client
// that has been disconnected for a long time. Older history beyond this is the
// concern of the REST event-log endpoints, not the live stream.
const resumeReplayLimit = 1000

// HandlerConfig groups the handler's injected collaborators. Redis, Organization, and
// Log are required; LogReader is optional (nil disables ?since= replay — the
// stream still tails live); Clock and Heartbeat default to system time / 20s.
type HandlerConfig struct {
	Redis        RedisClient
	Organization OrganizationAccessor
	LogReader    EventLogReader    // optional; nil => ?since= tails live only
	Principals   PrincipalAccessor // optional; nil => connections register with org id only
	Registry     *ConnRegistry     // optional; nil => streams are not droppable by the control bus
	Clock        Clock             // optional; nil => SystemClock
	Heartbeat    int               // optional heartbeat seconds; <=0 => 20s
	Log          *slog.Logger      // optional; nil => discard
}

// Handler serves GET /api/v1/events as a chunked application/x-ndjson stream
// (§9). One Handler is shared across all connections; per-request state is local.
type Handler struct {
	redis        RedisClient
	organization OrganizationAccessor
	logReader    EventLogReader
	principals   PrincipalAccessor
	registry     *ConnRegistry
	clock        Clock
	heartbeat    int
	log          *slog.Logger
}

// NewHandler builds a stream Handler from cfg, applying defaults for the optional
// fields. It panics if a required collaborator (Redis or Organization) is missing —
// that is a wiring bug, not a runtime condition.
func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.Redis == nil {
		panic("stream: HandlerConfig.Redis is required")
	}
	if cfg.Organization == nil {
		panic("stream: HandlerConfig.Organization is required")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = SystemClock
	}
	hb := cfg.Heartbeat
	if hb <= 0 {
		hb = int(heartbeatInterval.Seconds())
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Handler{
		redis:        cfg.Redis,
		organization: cfg.Organization,
		logReader:    cfg.LogReader,
		principals:   cfg.Principals,
		registry:     cfg.Registry,
		clock:        clock,
		heartbeat:    hb,
		log:          log,
	}
}

// ServeHTTP implements the NDJSON stream. Lifecycle:
//  1. authorize (organization from context) and parse query (session, events, since);
//  2. open the Redis subscription FIRST so no live event is missed between the
//     replay and the tail;
//  3. on ?since=, replay matching event_log entries oldest-first, remembering the
//     last replayed event id to skip its duplicate on the live tail;
//  4. tail the subscription, writing each passing event as one JSON line + flush,
//     emitting a ping line every heartbeat interval, until the client disconnects.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	organization, ok := h.organization.OrganizationFromContext(ctx)
	if !ok || organization == "" {
		writeError(w, http.StatusUnauthorized, domain.ErrUnauthorized("authentication required"))
		return
	}

	// Register this live connection so the control bus can drop it on revocation
	// (§4.6). Cancelling the derived context makes the tail loop return and the
	// stream close; a reconnect re-validates against MySQL and fails closed.
	if h.registry != nil {
		id := ConnIdentity{OrganizationID: organization}
		if h.principals != nil {
			if pid, pok := h.principals.IdentityFromContext(ctx); pok {
				id = pid
				if id.OrganizationID == "" {
					id.OrganizationID = organization
				}
			}
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		deregister := h.registry.register(id, cancel)
		defer deregister()
	}

	q := r.URL.Query()
	session := q.Get("session")
	filter := parseEventFilter(q.Get("events"))
	if filter.empty() {
		writeError(w, http.StatusBadRequest, domain.ErrValidation("events filter matches no event types"))
		return
	}
	since := q.Get("since")

	flusher, ok := w.(http.Flusher)
	if !ok {
		// Without flushing we cannot stream; this is an environment/wiring issue.
		writeError(w, http.StatusInternalServerError, domain.ErrInternal("streaming unsupported"))
		return
	}

	// Subscribe before any replay so the gap between "end of replay" and "start
	// of tail" carries no lost events — anything published during replay is
	// buffered on the subscription channel.
	pubsub := h.subscribe(ctx, organization, session)
	defer func() { _ = pubsub.Close() }()
	msgs := pubsub.Channel()

	h.writeHeaders(w)
	flusher.Flush() // commit status + headers so the client sees the stream open

	enc := newLineWriter(w, flusher)

	// Replay from the durable log for ?since=, tracking the last id we emitted so
	// the live tail can drop its echo.
	lastReplayed := ""
	if since != "" && h.logReader != nil {
		var err error
		lastReplayed, err = h.replaySince(ctx, enc, organization, session, since, filter)
		if err != nil {
			// The headers are already sent, so we cannot change the status code.
			// Surface the failure as a final stream line and stop.
			h.log.Error("stream replay failed", "organization", organization, "session", session, "since", since, "err", err)
			_ = enc.writeJSON(map[string]any{"event": "error", "error": "replay_failed"})
			return
		}
	}

	ticker := h.clock.NewTicker(durationSeconds(h.heartbeat))
	defer ticker.Stop()

	h.tail(ctx, enc, msgs, ticker, filter, lastReplayed)
}

// subscribe opens the right subscription: an exact per-session channel when a
// session filter is given, otherwise a organization-wide pattern across all sessions.
func (h *Handler) subscribe(ctx context.Context, organization, session string) *redis.PubSub {
	if session != "" {
		return h.redis.Subscribe(ctx, sessionChannel(organization, session))
	}
	return h.redis.PSubscribe(ctx, organizationPattern(organization))
}

// replaySince streams persisted events strictly after `since`, in order, that
// pass the filter. It returns the event id of the last entry it READ from the log
// (whether or not it passed the filter) so the live tail can dedup the boundary.
func (h *Handler) replaySince(ctx context.Context, enc *lineWriter, organization, session, since string, filter eventFilter) (string, error) {
	entries, err := h.logReader.ListSince(ctx, organization, session, since, resumeReplayLimit)
	if err != nil {
		return "", fmt.Errorf("list since %q: %w", since, err)
	}
	last := since
	for i := range entries {
		e := &entries[i]
		last = e.EventID
		if !filter.allows(e.Type) {
			continue
		}
		if err := enc.writeJSON(logEntryEnvelope(e)); err != nil {
			return last, fmt.Errorf("write replayed event %q: %w", e.EventID, err)
		}
	}
	return last, nil
}

// tail copies live events from the subscription to the client until the request
// context is cancelled (client disconnect / server shutdown). It writes a ping
// line on each heartbeat tick. afterID, when non-empty, suppresses the single
// event whose id equals it — the boundary event already delivered during replay.
func (h *Handler) tail(ctx context.Context, enc *lineWriter, msgs <-chan *redis.Message, ticker Ticker, filter eventFilter, afterID string) {
	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C():
			if err := enc.writeJSON(pingEnvelope()); err != nil {
				h.log.Debug("stream heartbeat write failed, closing", "err", err)
				return
			}

		case msg, ok := <-msgs:
			if !ok {
				// Subscription channel closed (Redis connection gone).
				return
			}
			id, typ, ok := peekEvent(msg.Payload)
			if !ok {
				h.log.Warn("dropping malformed event from pubsub", "channel", msg.Channel)
				continue
			}
			if afterID != "" && id == afterID {
				afterID = "" // boundary consumed; deliver everything from here on
				continue
			}
			if !filter.allows(typ) {
				continue
			}
			if err := enc.writeRaw([]byte(msg.Payload)); err != nil {
				h.log.Debug("stream write failed, closing", "err", err)
				return
			}
		}
	}
}

// writeHeaders sets the NDJSON streaming response headers.
func (h *Handler) writeHeaders(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("Content-Type", "application/x-ndjson")
	hd.Set("Cache-Control", "no-cache, no-transform")
	hd.Set("Connection", "keep-alive")
	// Defeat proxy buffering so chunks reach the client promptly.
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// writeError sends a §11 error envelope before the stream has started.
func writeError(w http.ResponseWriter, status int, apiErr *domain.APIError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(domain.ErrorBody{Error: apiErr})
}
