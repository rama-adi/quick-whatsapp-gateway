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

type AppInvalidator interface {
	InvalidateSession(sessionID string)
}

type GrantRevocationCache interface {
	MarkGrantRevoked(grantID string)
}

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

func NewAppChangeSubscriber(rdb *redis.Client, inv AppInvalidator, log *slog.Logger) *AppChangeSubscriber {
	return NewControlSubscriber(rdb, inv, nil, nil, log)
}

func NewControlSubscriber(rdb *redis.Client, inv AppInvalidator, rev GrantRevocationCache, pending *PendingStore, log *slog.Logger) *AppChangeSubscriber {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &AppChangeSubscriber{rdb: rdb, inv: inv, rev: rev, pending: pending, log: log}
}

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
