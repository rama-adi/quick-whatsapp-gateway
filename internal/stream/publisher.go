package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// Publisher is the EventSink the inbound pipeline (and anything else producing
// events) writes to. It serializes each domain.Event to its canonical JSON
// envelope and publishes it to the per-(organization, session) Redis channel, where
// live NDJSON connections pick it up.
//
// Note: the Publisher only fans out over pub/sub. Durable persistence to
// event_log (for ?since= resume) is a separate concern owned by the events/store
// layer; the handler reads that log via EventLogReader. Keeping the two apart
// means a fan-out failure never blocks the write path and vice versa.
type Publisher struct {
	redis RedisClient
	log   *slog.Logger
}

// NewPublisher constructs a Publisher. log may be nil (a discarding logger is used).
func NewPublisher(rc RedisClient, log *slog.Logger) *Publisher {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Publisher{redis: rc, log: log}
}

// Publish marshals the event and posts it to its organization+session channel. It is
// the method the EventSink consumer interface (defined by event producers)
// expects, so *Publisher satisfies that interface directly.
//
// An empty Organization is a programming error — without it the event cannot be
// addressed to any subscriber — so we reject it rather than silently dropping.
func (p *Publisher) Publish(ctx context.Context, e domain.Event) error {
	if e.Organization == "" {
		return fmt.Errorf("stream: cannot publish event %q with empty organization", e.ID)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("stream: marshal event %q: %w", e.ID, err)
	}
	ch := channelFor(e.Organization, e.Session)
	if err := p.redis.Publish(ctx, ch, data).Err(); err != nil {
		return fmt.Errorf("stream: publish event %q to %q: %w", e.ID, ch, err)
	}
	p.log.Debug("event published", "event_id", e.ID, "type", e.Type, "channel", ch)
	return nil
}
