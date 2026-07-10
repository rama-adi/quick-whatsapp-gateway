package oidp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

const (
	ChannelOIDPAppChanged   = "ctrl:oidp.app.changed"
	ChannelOIDPGrantRevoked = "ctrl:oidp.grant.revoked"
)

// AppInvalidator evicts cached OAuth-app routing for a changed session.
type AppInvalidator interface {
	InvalidateSession(sessionID string)
}

// GrantRevocationCache makes grant revocation visible before database/cache
// propagation completes.
type GrantRevocationCache interface {
	MarkGrantRevoked(grantID string)
}

// AppChangeSubscriber consumes OAuth control events until Stop is called. Start
// establishes the Redis subscription synchronously before returning, is
// idempotent, and detaches the loop from the caller's request cancellation.
type AppChangeSubscriber struct {
	rdb     *redis.Client
	inv     AppInvalidator
	rev     GrantRevocationCache
	pending *PendingStore
	log     *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	pubsub *redis.PubSub
}

// NewAppChangeSubscriber constructs an app-cache-only control subscriber.
func NewAppChangeSubscriber(rdb *redis.Client, inv AppInvalidator, log *slog.Logger) *AppChangeSubscriber {
	return NewControlSubscriber(rdb, inv, nil, nil, log)
}

// NewControlSubscriber constructs the full app-change and grant-revocation
// subscriber. Nil consumers disable their respective side effects.
func NewControlSubscriber(rdb *redis.Client, inv AppInvalidator, rev GrantRevocationCache, pending *PendingStore, log *slog.Logger) *AppChangeSubscriber {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &AppChangeSubscriber{rdb: rdb, inv: inv, rev: rev, pending: pending, log: log}
}

// Start confirms both Redis subscriptions before launching the background loop.
// Repeated calls while running are no-ops.
func (s *AppChangeSubscriber) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return nil
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	pubsub := s.rdb.Subscribe(loopCtx, ChannelOIDPAppChanged, ChannelOIDPGrantRevoked)
	if _, err := pubsub.Receive(loopCtx); err != nil {
		cancel()
		_ = pubsub.Close()
		return err
	}
	s.cancel, s.pubsub, s.done = cancel, pubsub, make(chan struct{})
	go s.loop(loopCtx, pubsub, s.done)
	return nil
}

// loop owns message consumption for one successful Start and closes done exactly
// once on cancellation or channel closure. It never holds s.mu while blocking on
// Redis, allowing Stop to cancel and close the subscription without deadlock.
func (s *AppChangeSubscriber) loop(ctx context.Context, pubsub *redis.PubSub, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-pubsub.Channel():
			if !ok {
				return
			}
			s.handle(msg.Channel, msg.Payload)
		}
	}
}

// handle validates one control payload and applies only local cache/state effects.
// Malformed or partially empty payloads are logged and isolated; Redis Pub/Sub has
// no retry guarantee, so SQL and bounded cache lifetimes remain authoritative.
func (s *AppChangeSubscriber) handle(channel, raw string) {
	switch channel {
	case ChannelOIDPAppChanged:
		var payload struct {
			SessionID string `json:"sessionId"`
			ClientID  string `json:"clientId"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			s.log.Warn("oidp app control bus: malformed payload", "err", err)
			return
		}
		if s.inv != nil {
			s.inv.InvalidateSession(payload.SessionID)
		}
		if s.pending != nil && payload.ClientID != "" {
			n, err := s.pending.DenyClientPending(context.Background(), payload.ClientID)
			if err != nil {
				s.log.Warn("oidp app control bus: deny pending failed", "clientId", payload.ClientID, "err", err)
			} else if n > 0 {
				s.log.Info("oidp app control bus: denied pending waits", "clientId", payload.ClientID, "count", n)
			}
		}
	case ChannelOIDPGrantRevoked:
		var payload struct {
			GrantID  string   `json:"grantId"`
			GrantIDs []string `json:"grantIds"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			s.log.Warn("oidp grant control bus: malformed payload", "err", err)
			return
		}
		if payload.GrantID != "" {
			payload.GrantIDs = append(payload.GrantIDs, payload.GrantID)
		}
		for _, id := range payload.GrantIDs {
			if s.rev != nil {
				s.rev.MarkGrantRevoked(id)
			}
		}
	}
}

// Stop cancels the subscription, closes Redis resources, and waits for the
// background loop. It is safe to call repeatedly.
func (s *AppChangeSubscriber) Stop() {
	s.mu.Lock()
	cancel, pubsub, done := s.cancel, s.pubsub, s.done
	s.cancel, s.pubsub, s.done = nil, nil, nil
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
