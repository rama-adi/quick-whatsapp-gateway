package oidp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

const ChannelOIDPAppChanged = "ctrl:oidp.app.changed"

type AppInvalidator interface {
	InvalidateSession(sessionID string)
}

type AppChangeSubscriber struct {
	rdb *redis.Client
	inv AppInvalidator
	log *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	pubsub *redis.PubSub
}

func NewAppChangeSubscriber(rdb *redis.Client, inv AppInvalidator, log *slog.Logger) *AppChangeSubscriber {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &AppChangeSubscriber{rdb: rdb, inv: inv, log: log}
}

func (s *AppChangeSubscriber) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return nil
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	pubsub := s.rdb.Subscribe(loopCtx, ChannelOIDPAppChanged)
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
			var payload struct {
				SessionID string `json:"sessionId"`
			}
			if err := json.Unmarshal([]byte(msg.Payload), &payload); err != nil {
				s.log.Warn("oidp app control bus: malformed payload", "err", err)
				continue
			}
			if s.inv != nil {
				s.inv.InvalidateSession(payload.SessionID)
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
