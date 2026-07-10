// Package stream implements live event delivery (masterplan §9) and the Redis
// pub/sub fan-out that backs it.
//
// Two halves live here:
//
//   - Publisher is the EventSink the rest of the system writes domain.Events to.
//     It marshals each event and PUBLISHes it to a per-(organization, session) Redis
//     channel. It runs in the gateway.
//
//   - Pump is the transport-agnostic subscribe → connected → replay → tail loop:
//     it subscribes per resolved Scope (session / organization / firehose), filters
//     by the events list, optionally replays from the event log on a since cursor,
//     then tails live events — emitting a heartbeat every ~20s and writing each
//     framed JSON message to a Sink, stopping when the context is cancelled or the
//     Sink errors. The central router owns the only Sink (a WebSocket text-frame
//     writer); the legacy NDJSON gateway transport has been removed.
//
// This package imports only the stdlib, go-redis (already a dependency), and
// internal/domain. Every other collaborator (the event-log reader, the
// clock/ticker, the Sink, the principal accessor) is a small consumer interface
// defined here and wired to a concrete type by the composition root.
package stream

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// RedisClient is the consumer interface over go-redis we need for fan-out. The
// concrete *redis.Client / redis.UniversalClient satisfies it as-is. We keep it
// tiny (publish + the two subscribe flavours) so it is trivial to fake — though
// in tests we use miniredis behind a real *redis.Client instead.
type RedisClient interface {
	// Publish posts a message to a single channel (used by the Publisher).
	Publish(ctx context.Context, channel string, message any) *redis.IntCmd
	// Subscribe opens a subscription to exact channel name(s) (one session).
	Subscribe(ctx context.Context, channels ...string) *redis.PubSub
	// PSubscribe opens a pattern subscription (all of a organization's sessions).
	PSubscribe(ctx context.Context, patterns ...string) *redis.PubSub
}

// EventLogReader replays persisted events for ?since= resume. The store package
// owns the concrete MySQL implementation; we depend on this consumer interface.
//
// ListSince returns event-log entries for the organization (optionally narrowed to a
// session, "" = all sessions) strictly AFTER the given event id, in ascending
// cursor order, capped at limit. afterEventID == "" means "from the beginning"
// (bounded by limit). The returned slice is ordered oldest-first so the handler
// can stream it in order before switching to the live tail.
type EventLogReader interface {
	ListSince(ctx context.Context, organization, session, afterEventID string, limit int) ([]domain.EventLogEntry, error)
}

// channelPrefix namespaces all stream pub/sub channels. The full channel for a
// single event is "<prefix><organization>:<session>"; a organization-wide pattern is
// "<prefix><organization>:*". Keeping organization first means a organization pattern never leaks
// another organization's events.
const channelPrefix = "evt:"

// channelFor returns the exact pub/sub channel an event is published to.
func channelFor(organization, session string) string {
	return fmt.Sprintf("%s%s:%s", channelPrefix, organization, session)
}

// sessionChannel returns the exact channel a single-session subscriber listens on.
func sessionChannel(organization, session string) string {
	return channelFor(organization, session)
}

// organizationPattern returns the glob pattern a organization-wide subscriber listens on
// (all of that organization's sessions). go-redis PSubscribe uses Redis glob syntax.
func organizationPattern(organization string) string {
	return fmt.Sprintf("%s%s:*", channelPrefix, escapeRedisGlob(organization))
}

// escapeRedisGlob quotes Redis pattern metacharacters in an organization id.
// Organization subscriptions use PSUBSCRIBE, so treating an id as a raw glob
// could accidentally widen a tenant-scoped stream.
func escapeRedisGlob(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch r {
		case '\\', '*', '?', '[', ']':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// firehosePattern returns the glob pattern the admin firehose subscribes on (every
// org, every session). Because events are published per (org, session), "all
// gateways" falls out of this naturally (D5a).
func firehosePattern() string {
	return channelPrefix + "*"
}
