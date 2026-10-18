package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

// historyStalledAfterMs marks a running import as stalled when nothing changed
// for this long (the phone went offline, or the gateway lost the session).
const historyStalledAfterMs = int64(10 * 60 * 1000)

// SetHistorySync supplies the history-run repository and the deployment's
// HISTORY_SYNC_DEFAULT_DAYS / HISTORY_SYNC_MAX_DAYS policy.
func (s *SessionService) SetHistorySync(history *store.HistoryRepo, defaultDays, maxDays int) {
	s.history = history
	s.historyDefaultDays = defaultDays
	s.historyMaxDays = maxDays
}

// resolveHistorySyncDays applies the default to an omitted value and rejects
// values outside 0..HISTORY_SYNC_MAX_DAYS.
func (s *SessionService) resolveHistorySyncDays(days *int) (int, error) {
	if days == nil {
		return min(s.historyDefaultDays, s.historyMaxDays), nil
	}
	if *days < 0 || *days > s.historyMaxDays {
		return 0, domain.ErrValidation(fmt.Sprintf("historySyncDays must be between 0 and %d", s.historyMaxDays))
	}
	return *days, nil
}

// UpdateInput carries the session settings a PATCH may change. Nil fields are
// left as they are.
type UpdateInput struct {
	HistorySyncDays *int
}

// Update changes session settings and returns the refreshed session. A
// history window change is streamed to the assigned gateway with the session
// configuration (it advances the gateway's desired revision) and applies at
// the next link or relink.
func (s *SessionService) Update(ctx context.Context, organizationID, id string, in UpdateInput) (domain.WASession, error) {
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return domain.WASession{}, err
	}
	if in.HistorySyncDays == nil {
		return sess, nil
	}
	days, err := s.resolveHistorySyncDays(in.HistorySyncDays)
	if err != nil {
		return domain.WASession{}, err
	}
	if days == sess.HistorySyncDays {
		return sess, nil
	}
	if s.assignments == nil {
		return domain.WASession{}, errLiveUnavailable()
	}
	if err := s.assignments.UpdateHistorySyncDays(ctx, id, days, domain.NowMs()); err != nil {
		return domain.WASession{}, err
	}
	return s.Get(ctx, organizationID, id)
}

// HistorySync returns the session's history import setting, relink detection
// and latest import run.
func (s *SessionService) HistorySync(ctx context.Context, organizationID, id string) (domain.HistorySyncStatus, error) {
	sess, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return domain.HistorySyncStatus{}, err
	}
	out := domain.HistorySyncStatus{
		HistorySyncDays:  min(sess.HistorySyncDays, s.historyMaxDays),
		AccountJID:       sess.DataAccountJID,
		AccountChanged:   sess.AccountChangedAt != nil,
		AccountChangedAt: sess.AccountChangedAt,
	}
	if s.history == nil {
		return out, nil
	}
	run, err := s.history.LatestRun(ctx, id)
	if err != nil {
		var apiErr *domain.APIError
		if errors.As(err, &apiErr) && apiErr.Code == domain.CodeNotFound {
			return out, nil
		}
		return domain.HistorySyncStatus{}, err
	}
	run.Stalled = run.Status == "running" && domain.NowMs()-run.UpdatedAt > historyStalledAfterMs
	out.Run = &run
	return out, nil
}
