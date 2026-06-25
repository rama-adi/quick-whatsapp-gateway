package service

import (
	"context"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/webhooks"
)

// This file adapts the store webhook repos to the consumer interfaces declared
// by internal/webhooks. The store's method shapes differ slightly (the delivery
// repo computes attempts++ internally and has no eventType filter on the webhook
// listing), so these thin wrappers reconcile the two without changing either.

// WebhookRepoAdapter adapts *store.WebhookRepo to webhooks.WebhookRepo.
type WebhookRepoAdapter struct {
	repo *store.WebhookRepo
}

// NewWebhookRepoAdapter wraps a store.WebhookRepo for the webhooks subsystem.
func NewWebhookRepoAdapter(repo *store.WebhookRepo) *WebhookRepoAdapter {
	return &WebhookRepoAdapter{repo: repo}
}

var _ webhooks.WebhookRepo = (*WebhookRepoAdapter)(nil)

// ListMatching returns the candidate webhooks for (tenant, session); the events
// filter is applied by the dispatcher/enqueuer via webhooks.EventMatches.
func (a *WebhookRepoAdapter) ListMatching(ctx context.Context, tenant, session, eventType string) ([]domain.Webhook, error) {
	hooks, err := a.repo.ListActiveForEvent(ctx, tenant, session)
	if err != nil {
		return nil, err
	}
	out := hooks[:0]
	for _, h := range hooks {
		if webhooks.EventMatches(h.Events, eventType) {
			out = append(out, h)
		}
	}
	return out, nil
}

// Get loads a webhook by id.
func (a *WebhookRepoAdapter) Get(ctx context.Context, id string) (domain.Webhook, error) {
	return a.repo.Get(ctx, id)
}

// WebhookDeliveryRepoAdapter adapts *store.WebhookDeliveryRepo to
// webhooks.WebhookDeliveryRepo. The store repo computes attempts++ itself, so the
// adapter drops the caller-supplied attempts count.
type WebhookDeliveryRepoAdapter struct {
	repo *store.WebhookDeliveryRepo
}

// NewWebhookDeliveryRepoAdapter wraps a store.WebhookDeliveryRepo.
func NewWebhookDeliveryRepoAdapter(repo *store.WebhookDeliveryRepo) *WebhookDeliveryRepoAdapter {
	return &WebhookDeliveryRepoAdapter{repo: repo}
}

var _ webhooks.WebhookDeliveryRepo = (*WebhookDeliveryRepoAdapter)(nil)

func (a *WebhookDeliveryRepoAdapter) Create(ctx context.Context, d *domain.WebhookDelivery) error {
	return a.repo.Create(ctx, d)
}

func (a *WebhookDeliveryRepoAdapter) ClaimDue(ctx context.Context, now int64, limit int) ([]domain.WebhookDelivery, error) {
	return a.repo.ClaimDue(ctx, now, limit)
}

func (a *WebhookDeliveryRepoAdapter) MarkDelivered(ctx context.Context, id uint64, _ int, responseCode int) error {
	return a.repo.MarkDelivered(ctx, id, responseCode)
}

func (a *WebhookDeliveryRepoAdapter) MarkFailed(ctx context.Context, id uint64, _ int, nextRetryAt int64, responseCode *int, lastErr string) error {
	return a.repo.MarkFailed(ctx, id, responseCode, lastErr, nextRetryAt)
}

func (a *WebhookDeliveryRepoAdapter) MarkDead(ctx context.Context, id uint64, _ int, responseCode *int, lastErr string) error {
	return a.repo.MarkDead(ctx, id, responseCode, lastErr)
}

func (a *WebhookDeliveryRepoAdapter) ExistsTerminal(ctx context.Context, webhookID, eventID string) (bool, error) {
	return a.repo.ExistsTerminal(ctx, webhookID, eventID)
}
