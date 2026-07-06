package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// missingVoterKeyPrefix marks synthetic per-row keys for legacy poll_votes rows
// stored with an empty voter_lid, so they still count as distinct voters in the
// aggregate without leaking a fake id into the public voters list.
const missingVoterKeyPrefix = "__missing_voter_lid:"

type pollRecapPublisher interface {
	Publish(ctx context.Context, e domain.Event) error
}

type pollRecapEnqueuer interface {
	Enqueue(ctx context.Context, evt domain.Event) (int, error)
}

// PollRecapWorker emits one poll.recap event after a poll's configured end_time.
// MySQL is the durable source of truth and Redis is only a low-latency timer
// index: if Redis is empty, the periodic DB sweep still catches due recaps.
type PollRecapWorker struct {
	polls     *store.PollRepo
	votes     *store.PollVoteRepo
	ids       *store.IdentityRepo
	eventLog  *store.EventLogRepo
	publisher pollRecapPublisher
	webhooks  pollRecapEnqueuer
	redis     *redis.Client
	clock     func() int64
	log       *slog.Logger
	key       string
	interval  time.Duration
	limit     int
}

type PollRecapConfig struct {
	RedisPrefix string
	Interval    time.Duration
	Limit       int
	Clock       func() int64
	Log         *slog.Logger
}

func NewPollRecapWorker(st *store.Store, publisher pollRecapPublisher, webhooks pollRecapEnqueuer, rdb *redis.Client, cfg PollRecapConfig) *PollRecapWorker {
	if cfg.Clock == nil {
		cfg.Clock = domain.NowMs
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.Limit <= 0 {
		cfg.Limit = 100
	}
	prefix := strings.TrimSuffix(cfg.RedisPrefix, ":")
	if prefix == "" {
		prefix = "gw"
	}
	return &PollRecapWorker{
		polls:     st.Polls,
		votes:     st.PollVotes,
		ids:       st.Identities,
		eventLog:  st.EventLog,
		publisher: publisher,
		webhooks:  webhooks,
		redis:     rdb,
		clock:     cfg.Clock,
		log:       cfg.Log,
		key:       prefix + ":poll:recap:due",
		interval:  cfg.Interval,
		limit:     cfg.Limit,
	}
}

func (w *PollRecapWorker) Start(ctx context.Context) func() {
	workerCtx, cancel := context.WithCancel(ctx)
	go w.loop(workerCtx)
	return cancel
}

func (w *PollRecapWorker) Schedule(ctx context.Context, sessionID, pollMessageID string, endTimeMs int64) {
	if w == nil || w.redis == nil || endTimeMs <= 0 {
		return
	}
	member := sessionID + "|" + pollMessageID
	if err := w.redis.ZAdd(ctx, w.key, redis.Z{Score: float64(endTimeMs), Member: member}).Err(); err != nil {
		w.log.WarnContext(ctx, "schedule poll recap in redis failed", "session", sessionID, "message_id", pollMessageID, "err", err)
	}
}

func (w *PollRecapWorker) loop(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.processDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processDue(ctx)
		}
	}
}

func (w *PollRecapWorker) processDue(ctx context.Context) {
	now := w.clock()
	if w.redis != nil {
		_, err := w.redis.ZRemRangeByScore(ctx, w.key, "-inf", fmt.Sprintf("%d", now)).Result()
		if err != nil {
			w.log.WarnContext(ctx, "poll recap redis due check failed", "err", err)
		}
	}
	due, err := w.polls.ListDueRecaps(ctx, now, w.limit)
	if err != nil {
		w.log.WarnContext(ctx, "list due poll recaps failed", "err", err)
		return
	}
	for _, poll := range due {
		if err := w.emitOne(ctx, poll); err != nil {
			w.log.WarnContext(ctx, "emit poll recap failed", "session", poll.SessionID, "message_id", poll.PollMessageID, "err", err)
		}
	}
}

func (w *PollRecapWorker) emitOne(ctx context.Context, poll domain.PollRecapCandidate) error {
	now := w.clock()
	claimed, err := w.polls.MarkRecapEmitted(ctx, poll.SessionID, poll.PollMessageID, now)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}

	payload, err := w.buildPayload(ctx, poll)
	if err != nil {
		return err
	}
	evt := domain.NewEvent(domain.EventPollRecap, poll.SessionID, poll.OrganizationID, payload)
	raw, err := json.Marshal(evt.Payload)
	if err != nil {
		return fmt.Errorf("marshal poll recap payload: %w", err)
	}
	if _, err := w.eventLog.Append(ctx, domain.EventLogEntry{
		EventID:        evt.ID,
		OrganizationID: evt.Organization,
		SessionID:      evt.Session,
		Type:           evt.Type,
		Payload:        raw,
		CreatedAt:      evt.Timestamp,
	}); err != nil {
		return err
	}
	if err := w.publisher.Publish(ctx, evt); err != nil {
		w.log.WarnContext(ctx, "publish poll recap failed", "event_id", evt.ID, "err", err)
	}
	if _, err := w.webhooks.Enqueue(ctx, evt); err != nil {
		w.log.WarnContext(ctx, "enqueue poll recap webhooks failed", "event_id", evt.ID, "err", err)
	}
	return nil
}

func (w *PollRecapWorker) buildPayload(ctx context.Context, poll domain.PollRecapCandidate) (domain.PollRecapPayload, error) {
	votes, err := w.votes.ListByPoll(ctx, poll.SessionID, poll.PollMessageID)
	if err != nil {
		return domain.PollRecapPayload{}, err
	}
	names := map[string]string(nil)
	if !poll.HideVotes && w.ids != nil {
		voterIDs := pollRecapVoterIDs(votes)
		names, err = w.ids.NamesForMentions(ctx, voterIDs)
		if err != nil {
			return domain.PollRecapPayload{}, err
		}
	}
	return buildPollRecapPayload(poll, votes, names)
}

func buildPollRecapPayload(poll domain.PollRecapCandidate, votes []domain.PollVote, names map[string]string) (domain.PollRecapPayload, error) {
	latest := make(map[string][]string)
	for i, vote := range votes {
		var selected []string
		if len(vote.SelectedOptions) > 0 {
			if err := json.Unmarshal(vote.SelectedOptions, &selected); err != nil {
				return domain.PollRecapPayload{}, fmt.Errorf("decode poll vote selection: %w", err)
			}
		}
		voterKey := vote.VoterLID
		if voterKey == "" {
			voterKey = fmt.Sprintf("%s%d:%d", missingVoterKeyPrefix, vote.ID, i)
		}
		latest[voterKey] = selected
	}

	counts := make(map[string]int, len(poll.Options))
	for _, option := range poll.Options {
		counts[option] = 0
	}
	for _, selected := range latest {
		for _, option := range selected {
			counts[option]++
		}
	}
	options := make([]domain.PollRecapOption, 0, len(poll.Options))
	for _, option := range poll.Options {
		options = append(options, domain.PollRecapOption{Option: option, Count: counts[option]})
	}
	var voters []domain.PollRecapVoter
	if !poll.HideVotes {
		voterIDs := make([]string, 0, len(latest))
		for voterID := range latest {
			voterIDs = append(voterIDs, voterID)
		}
		sort.Strings(voterIDs)
		voters = make([]domain.PollRecapVoter, 0, len(voterIDs))
		for _, voterID := range voterIDs {
			// Synthetic keys stand in for legacy rows persisted without a voter
			// identity; they count toward the totals but carry no publishable id.
			if strings.HasPrefix(voterID, missingVoterKeyPrefix) {
				continue
			}
			voters = append(voters, domain.PollRecapVoter{
				VoterID:         voterID,
				DisplayName:     names[jidUserPart(voterID)],
				SelectedOptions: latest[voterID],
			})
		}
	}
	return domain.PollRecapPayload{
		PollMessageID:   poll.PollMessageID,
		ChatJID:         poll.ChatJID,
		Name:            poll.Name,
		Options:         options,
		SelectableCount: poll.SelectableCount,
		EndTime:         poll.EndTime,
		HideVotes:       poll.HideVotes,
		TotalVotes:      len(latest),
		Voters:          voters,
	}, nil
}

func pollRecapVoterIDs(votes []domain.PollVote) []string {
	out := make([]string, 0, len(votes))
	seen := make(map[string]struct{}, len(votes))
	for _, vote := range votes {
		if vote.VoterLID == "" {
			continue
		}
		if _, ok := seen[vote.VoterLID]; ok {
			continue
		}
		seen[vote.VoterLID] = struct{}{}
		out = append(out, vote.VoterLID)
	}
	return out
}
