// Package controlbus implements the gateway's subscriber on the cross-service
// Redis control bus (masterplan §4.6). The frontend PUBLISHes low-volume ctrl:*
// messages when an api-key is revoked or a user is banned/removed from an org;
// every gateway SUBSCRIBEs and reacts by evicting its positive api-key cache and
// dropping any live NDJSON streams authenticated by that key/user/org.
//
// Delivery is fire-and-forget (Redis pub/sub): a gateway that is down when a
// message is published misses it, which the 60s cache TTL backstop covers
// (§4.6). The channel names are GLOBAL — literally "ctrl:apikey.revoked" etc. —
// and are NOT namespaced by REDIS_PREFIX, because the bus fans out to the
// frontend and every gateway regardless of stack prefix.
package controlbus

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

// Control-bus channels (§4.6). Global, un-prefixed: the frontend and all gateways
// agree on these literal names.
const (
	ChannelAPIKeyRevoked = "ctrl:apikey.revoked"
	ChannelUserBanned    = "ctrl:user.banned"
	ChannelMemberRemoved = "ctrl:member.removed"
)

// KeyCache is the slice of the api-key positive cache the subscriber evicts on a
// control message. *authz.CachingKeyVerifier satisfies it. Consumer-defined here
// so controlbus does not import authz.
type KeyCache interface {
	EvictKey(keyID string)
	EvictUser(userID string)
	EvictUserOrg(userID, orgID string)
}

// StreamDropper is the slice of the live-connection registry the subscriber calls
// to close streams authenticated by a revoked key/user/org. *stream.ConnRegistry
// satisfies it. Consumer-defined here so controlbus does not import stream.
type StreamDropper interface {
	DropByKey(keyID string) int
	DropByUser(userID string) int
	DropByUserOrg(userID, orgID string) int
}

// Subscriber wires a Redis pub/sub subscription on the ctrl:* channels to the
// local cache + stream registry. Construct with New, then Start (spawns the
// receive loop) and Stop (clean shutdown) it from the composition root.
type Subscriber struct {
	rdb     *redis.Client
	cache   KeyCache
	dropper StreamDropper
	log     *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	pubsub *redis.PubSub
}

// New builds a Subscriber. cache and dropper may each be nil (that side of the
// reaction is skipped); log may be nil (a discarding logger is used).
func New(rdb *redis.Client, cache KeyCache, dropper StreamDropper, log *slog.Logger) *Subscriber {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Subscriber{rdb: rdb, cache: cache, dropper: dropper, log: log}
}

// ctrlMessage is the union JSON shape of every ctrl:* payload (§4.6). Each channel
// uses a subset: apikey.revoked → keyId(+userId); user.banned → userId;
// member.removed → userId+organizationId.
type ctrlMessage struct {
	KeyID          string `json:"keyId"`
	UserID         string `json:"userId"`
	OrganizationID string `json:"organizationId"`
}

// Start opens the subscription and runs the receive loop in a goroutine. It
// returns once the subscription is confirmed (so a publish immediately after
// Start is not missed). Calling Start twice is a no-op after the first.
func (s *Subscriber) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return nil // already started
	}
	if s.rdb == nil {
		return errors.New("controlbus: redis client must not be nil")
	}

	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	pubsub := s.rdb.Subscribe(loopCtx, ChannelAPIKeyRevoked, ChannelUserBanned, ChannelMemberRemoved)
	// Confirm the subscription before returning so an immediately-following
	// publish is delivered (avoids a startup race in tests and at boot).
	if _, err := pubsub.Receive(loopCtx); err != nil {
		cancel()
		_ = pubsub.Close()
		return err
	}

	s.cancel = cancel
	s.pubsub = pubsub
	s.done = make(chan struct{})
	go s.loop(loopCtx, pubsub, s.done)
	s.log.Info("control bus subscriber started",
		"channels", []string{ChannelAPIKeyRevoked, ChannelUserBanned, ChannelMemberRemoved})
	return nil
}

// loop consumes messages until the context is cancelled or the channel closes.
func (s *Subscriber) loop(ctx context.Context, pubsub *redis.PubSub, done chan struct{}) {
	defer close(done)
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			s.handle(msg.Channel, msg.Payload)
		}
	}
}

// handle decodes one control message and applies its eviction + stream drop. A
// malformed payload is logged and ignored (fire-and-forget; the cache TTL is the
// backstop) rather than crashing the loop.
func (s *Subscriber) handle(channel, payload string) {
	var m ctrlMessage
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		s.log.Warn("control bus: dropping malformed message", "channel", channel, "err", err)
		return
	}
	switch channel {
	case ChannelAPIKeyRevoked:
		if m.KeyID == "" {
			s.log.Warn("control bus: apikey.revoked missing keyId", "channel", channel)
			return
		}
		if s.cache != nil {
			s.cache.EvictKey(m.KeyID)
		}
		var dropped int
		if s.dropper != nil {
			dropped = s.dropper.DropByKey(m.KeyID)
		}
		s.log.Info("control bus: api-key revoked", "keyId", m.KeyID, "streamsDropped", dropped)
	case ChannelUserBanned:
		if m.UserID == "" {
			s.log.Warn("control bus: user.banned missing userId", "channel", channel)
			return
		}
		if s.cache != nil {
			s.cache.EvictUser(m.UserID)
		}
		var dropped int
		if s.dropper != nil {
			dropped = s.dropper.DropByUser(m.UserID)
		}
		s.log.Info("control bus: user banned", "userId", m.UserID, "streamsDropped", dropped)
	case ChannelMemberRemoved:
		if m.UserID == "" || m.OrganizationID == "" {
			s.log.Warn("control bus: member.removed missing userId/organizationId", "channel", channel)
			return
		}
		if s.cache != nil {
			s.cache.EvictUserOrg(m.UserID, m.OrganizationID)
		}
		var dropped int
		if s.dropper != nil {
			dropped = s.dropper.DropByUserOrg(m.UserID, m.OrganizationID)
		}
		s.log.Info("control bus: member removed", "userId", m.UserID, "organizationId", m.OrganizationID, "streamsDropped", dropped)
	default:
		s.log.Warn("control bus: message on unexpected channel", "channel", channel)
	}
}

// Stop cancels the receive loop and closes the subscription, blocking until the
// goroutine has exited. Safe to call when never started.
func (s *Subscriber) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	pubsub := s.pubsub
	done := s.done
	s.cancel = nil
	s.pubsub = nil
	s.done = nil
	s.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	if pubsub != nil {
		_ = pubsub.Close()
	}
	if done != nil {
		<-done
	}
}
