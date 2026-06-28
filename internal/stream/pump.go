package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Sink is the transport-agnostic output of a live event subscription. The pump
// produces framed JSON payloads (one event/ping/connected envelope each) and the
// adapter writes them on its wire — a WebSocket text frame on the router, an
// NDJSON line on the legacy gateway handler. Send must be safe to call from the
// pump's single goroutine; a returned error ends the pump (client gone).
type Sink interface {
	Send(ctx context.Context, payload []byte) error
}

// Scope is the resolved subscription a pump fans out. Exactly one shape:
//   - firehose:        Organization == ""              → all orgs/sessions (evt:*)
//   - organization:    Organization set, Session == "" → evt:{org}:*
//   - single session:  Organization + Session set      → evt:{org}:{session}
//
// GatewaySource, when set on a firehose scope, filters to events produced by one
// gateway (requires the envelope to carry the source gateway; reserved for when
// that tag exists — see eventing.md).
type Scope struct {
	Organization  string
	Session       string
	GatewaySource string
}

// PumpConfig wires a Pump. Redis is required; LogReader enables ?since= replay
// (org/session scopes only); Clock/Heartbeat/Log default sensibly.
type PumpConfig struct {
	Redis     RedisClient
	LogReader EventLogReader
	Clock     Clock
	Heartbeat int
	Log       *slog.Logger
}

// Pump runs the subscribe → connected → replay → tail loop of a live event
// subscription, writing to a Sink. It is the transport-agnostic core shared by the
// router's WebSocket realtime endpoint (D5) and the legacy NDJSON handler.
type Pump struct {
	redis     RedisClient
	logReader EventLogReader
	clock     Clock
	heartbeat int
	log       *slog.Logger
}

// NewPump builds a Pump, applying defaults. It panics if Redis is missing (a
// wiring bug, not a runtime condition).
func NewPump(cfg PumpConfig) *Pump {
	if cfg.Redis == nil {
		panic("stream: PumpConfig.Redis is required")
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
	return &Pump{redis: cfg.Redis, logReader: cfg.LogReader, clock: clock, heartbeat: hb, log: log}
}

// Run subscribes per scope, emits a connected frame, replays from the durable log
// when since is set (org/session scopes only — firehose is live-tail), then tails
// live events until ctx is cancelled or the Sink errors. events is the type
// allow-list ("*"/empty = all).
func (p *Pump) Run(ctx context.Context, sink Sink, scope Scope, events []string, since string) error {
	filter := parseEventFilter(strings.Join(events, ","))
	if filter.empty() {
		return fmt.Errorf("stream: event filter matches no types")
	}

	pubsub := p.subscribe(ctx, scope)
	defer func() { _ = pubsub.Close() }()
	msgs := pubsub.Channel()

	if err := p.send(ctx, sink, connectedEnvelope(p.heartbeat)); err != nil {
		return err
	}

	lastReplayed := ""
	if since != "" && p.logReader != nil && scope.Organization != "" {
		var err error
		lastReplayed, err = p.replay(ctx, sink, scope, since, filter)
		if err != nil {
			p.log.Error("stream replay failed", "organization", scope.Organization, "session", scope.Session, "since", since, "err", err)
			_ = p.send(ctx, sink, map[string]any{"event": "error", "error": "replay_failed"})
			return err
		}
	}

	ticker := p.clock.NewTicker(durationSeconds(p.heartbeat))
	defer ticker.Stop()
	return p.tail(ctx, sink, msgs, ticker, filter, lastReplayed)
}

func (p *Pump) subscribe(ctx context.Context, scope Scope) *redis.PubSub {
	switch {
	case scope.Organization == "":
		return p.redis.PSubscribe(ctx, firehosePattern())
	case scope.Session != "":
		return p.redis.Subscribe(ctx, sessionChannel(scope.Organization, scope.Session))
	default:
		return p.redis.PSubscribe(ctx, organizationPattern(scope.Organization))
	}
}

func (p *Pump) replay(ctx context.Context, sink Sink, scope Scope, since string, filter eventFilter) (string, error) {
	entries, err := p.logReader.ListSince(ctx, scope.Organization, scope.Session, since, resumeReplayLimit)
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
		if err := p.send(ctx, sink, logEntryEnvelope(e)); err != nil {
			return last, err
		}
	}
	return last, nil
}

func (p *Pump) tail(ctx context.Context, sink Sink, msgs <-chan *redis.Message, ticker Ticker, filter eventFilter, afterID string) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C():
			if err := p.send(ctx, sink, pingEnvelope()); err != nil {
				return err
			}
		case msg, ok := <-msgs:
			if !ok {
				return nil
			}
			id, typ, ok := peekEvent(msg.Payload)
			if !ok {
				p.log.Warn("dropping malformed event from pubsub", "channel", msg.Channel)
				continue
			}
			if afterID != "" && id == afterID {
				afterID = ""
				continue
			}
			if !filter.allows(typ) {
				continue
			}
			if err := sink.Send(ctx, []byte(msg.Payload)); err != nil {
				return err
			}
		}
	}
}

func (p *Pump) send(ctx context.Context, sink Sink, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return sink.Send(ctx, data)
}
