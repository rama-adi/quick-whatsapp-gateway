package service

import (
	"context"
	"errors"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// OutboxRepoAdapter adapts *store.OutboxRepo to outbound.OutboxRepo. The store
// repo uses value types, a not_found-on-miss idempotency lookup, a pointer-arg
// UpdateStatus and a (limit, updatedAt) ClaimQueued; the outbound consumer
// interface uses pointer entries, a (nil,nil)-on-miss lookup, string-arg
// UpdateStatus and a (sessionID, limit) ClaimQueued. This bridges the two.
type OutboxRepoAdapter struct {
	repo  *store.OutboxRepo
	clock func() int64
}

// NewOutboxRepoAdapter wraps a store.OutboxRepo for the outbound.Sender. clock
// may be nil (domain.NowMs is used).
func NewOutboxRepoAdapter(repo *store.OutboxRepo, clock func() int64) *OutboxRepoAdapter {
	if clock == nil {
		clock = domain.NowMs
	}
	return &OutboxRepoAdapter{repo: repo, clock: clock}
}

var _ outbound.OutboxRepo = (*OutboxRepoAdapter)(nil)

func (a *OutboxRepoAdapter) Insert(ctx context.Context, e *domain.OutboxEntry) error {
	return a.repo.Insert(ctx, *e)
}

func (a *OutboxRepoAdapter) GetByIdempotencyKey(ctx context.Context, organizationID, key string) (*domain.OutboxEntry, error) {
	e, err := a.repo.GetByIdempotency(ctx, organizationID, key)
	if err != nil {
		var apiErr *domain.APIError
		if errors.As(err, &apiErr) && apiErr.Code == domain.CodeNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

func (a *OutboxRepoAdapter) UpdateStatus(ctx context.Context, id string, status domain.OutboxStatus, waMessageID, errMsg string) error {
	var waPtr, errPtr *string
	if waMessageID != "" {
		waPtr = &waMessageID
	}
	if errMsg != "" {
		errPtr = &errMsg
	}
	return a.repo.UpdateStatus(ctx, id, status, waPtr, errPtr, a.clock())
}

func (a *OutboxRepoAdapter) ClaimQueued(ctx context.Context, sessionID string, limit int) ([]*domain.OutboxEntry, error) {
	rows, err := a.repo.ClaimQueued(ctx, limit, a.clock())
	if err != nil {
		return nil, err
	}
	out := make([]*domain.OutboxEntry, 0, len(rows))
	for i := range rows {
		// Filter to the requested session (the store claims across sessions).
		if sessionID != "" && rows[i].SessionID != sessionID {
			continue
		}
		e := rows[i]
		out = append(out, &e)
	}
	return out, nil
}
