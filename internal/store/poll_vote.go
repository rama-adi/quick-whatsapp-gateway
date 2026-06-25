package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// PollVoteRepo is the repository for poll_votes (§5/§7), appended on each
// DecryptPollVote event.
type PollVoteRepo struct {
	db dbExecQuerier
}

// NewPollVoteRepo constructs a PollVoteRepo.
func NewPollVoteRepo(db dbExecQuerier) *PollVoteRepo { return &PollVoteRepo{db: db} }

const pollVoteCols = `id, session_id, poll_message_id, voter_lid, selected_options, timestamp, raw_json`

func scanPollVote(s rowScanner) (domain.PollVote, error) {
	var (
		v        domain.PollVote
		selected []byte
		rawJSON  []byte
	)
	err := s.Scan(&v.ID, &v.SessionID, &v.PollMessageID, &v.VoterLID, &selected, &v.Timestamp, &rawJSON)
	if err != nil {
		return domain.PollVote{}, err
	}
	if len(selected) > 0 {
		v.SelectedOptions = append([]byte(nil), selected...)
	}
	if len(rawJSON) > 0 {
		v.RawJSON = append([]byte(nil), rawJSON...)
	}
	return v, nil
}

// Insert appends a poll vote. The poll_votes table has no unique key (a voter
// can re-vote and we keep the history), so this is a plain insert. Returns the
// auto-increment id.
func (r *PollVoteRepo) Insert(ctx context.Context, v domain.PollVote) (uint64, error) {
	const q = `INSERT INTO poll_votes
(session_id, poll_message_id, voter_lid, selected_options, timestamp, raw_json)
VALUES (?, ?, ?, ?, ?, ?)`
	res, err := r.db.ExecContext(ctx, q,
		v.SessionID, v.PollMessageID, v.VoterLID, []byte(v.SelectedOptions), v.Timestamp, nullableJSON(v.RawJSON),
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert poll vote: %w", err)
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
	q := "SELECT " + pollVoteCols + " FROM poll_votes WHERE session_id = ? AND poll_message_id = ? ORDER BY id ASC"
	rows, err := r.db.QueryContext(ctx, q, sessionID, pollMessageID)
	if err != nil {
		return nil, fmt.Errorf("store: list poll votes: %w", err)
	}
	defer rows.Close()
	var out []domain.PollVote
	for rows.Next() {
		v, err := scanPollVote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
