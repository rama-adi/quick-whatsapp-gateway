package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// PollRepo is the repository for polls (§5/§7): the option list of each
// poll-creation message, recorded so a later poll vote — which carries only
// SHA-256 hashes of the chosen options — can be resolved back to option text.
type PollRepo struct {
	db dbExecQuerier
	q  *storedb.Queries
}

// NewPollRepo constructs a PollRepo.
func NewPollRepo(db storedb.DBTX) *PollRepo {
	return &PollRepo{db: db, q: storedb.New(db)}
}

// Upsert records (or refreshes) a poll's options, keyed by session + poll
// message id. Re-receiving the same poll creation is a no-op on the immutable
// fields and just bumps updated_at.
func (r *PollRepo) Upsert(ctx context.Context, p domain.Poll) error {
	chatJID, err := canonicalDMChatJID(ctx, r.db, p.ChatJID)
	if err != nil {
		return err
	}
	p.ChatJID = chatJID

	options, err := json.Marshal(p.Options)
	if err != nil {
		return fmt.Errorf("store: marshal poll options: %w", err)
	}
	err = r.q.UpsertPoll(ctx, storedb.UpsertPollParams{
		SessionID:       p.SessionID,
		PollMessageID:   p.PollMessageID,
		ChatJid:         p.ChatJID,
		Name:            nullString(&p.Name),
		Options:         options,
		SelectableCount: int32(p.SelectableCount),
		CreatedAt:       p.CreatedAt,
		UpdatedAt:       p.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("store: upsert poll: %w", err)
	}
	return nil
}

// GetOptions returns a poll's options in creation order, or (nil, nil) when the
// poll is unknown (e.g. created before this session saw it). Callers treat the
// empty result as "cannot resolve" and fall back to the raw vote hashes.
func (r *PollRepo) GetOptions(ctx context.Context, sessionID, pollMessageID string) ([]string, error) {
	raw, err := r.q.GetPollOptions(ctx, storedb.GetPollOptionsParams{
		SessionID:     sessionID,
		PollMessageID: pollMessageID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get poll options: %w", err)
	}
	var options []string
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &options); err != nil {
			return nil, fmt.Errorf("store: decode poll options: %w", err)
		}
	}
	return options, nil
}
