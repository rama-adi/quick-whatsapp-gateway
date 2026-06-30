package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// PollVoteRepo is the repository for poll_votes (§5/§7), appended on each
// DecryptPollVote event.
type PollVoteRepo struct {
	q *storedb.Queries
}

// NewPollVoteRepo constructs a PollVoteRepo.
func NewPollVoteRepo(db storedb.DBTX) *PollVoteRepo { return &PollVoteRepo{q: storedb.New(db)} }

func pollVoteFromRow(row storedb.PollVote) domain.PollVote {
	v := domain.PollVote{
		ID:            row.ID,
		SessionID:     row.SessionID,
		PollMessageID: row.PollMessageID,
		VoterLID:      row.VoterLid,
		Timestamp:     row.Timestamp,
	}
	if len(row.SelectedOptions) > 0 {
		v.SelectedOptions = append([]byte(nil), row.SelectedOptions...)
	}
	if len(row.RawJson) > 0 {
		v.RawJSON = append([]byte(nil), row.RawJson...)
	}
	return v
}

// Insert appends a poll vote idempotently. A voter can re-vote and we keep the
// history, but a replay of the same WhatsApp poll-update event has the same
// poll/voter/timestamp tuple and is ignored by the schema unique key.
func (r *PollVoteRepo) Insert(ctx context.Context, v domain.PollVote) (uint64, error) {
	res, err := r.q.InsertPollVote(ctx, storedb.InsertPollVoteParams{
		SessionID:       v.SessionID,
		PollMessageID:   v.PollMessageID,
		VoterLid:        v.VoterLID,
		SelectedOptions: []byte(v.SelectedOptions),
		Timestamp:       v.Timestamp,
		RawJson:         nullableJSON(v.RawJSON),
	})
	if err != nil {
		return 0, fmt.Errorf("store: insert poll vote: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: insert poll vote affected: %w", err)
	}
	if affected == 0 {
		return 0, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert poll vote id: %w", err)
	}
	return uint64(id), nil
}

// ListByPoll returns all votes for a poll on a session, ordered by id (vote
// arrival order).
func (r *PollVoteRepo) ListByPoll(ctx context.Context, sessionID, pollMessageID string) ([]domain.PollVote, error) {
	rows, err := r.q.ListPollVotesByPoll(ctx, storedb.ListPollVotesByPollParams{
		SessionID:     sessionID,
		PollMessageID: pollMessageID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: list poll votes: %w", err)
	}
	out := make([]domain.PollVote, 0, len(rows))
	for _, row := range rows {
		out = append(out, pollVoteFromRow(row))
	}
	return out, nil
}
