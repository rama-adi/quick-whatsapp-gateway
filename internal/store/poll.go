package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// PollRepo is the repository for polls (§5/§7): the option list of each
// poll-creation message, recorded so a later poll vote — which carries only
// SHA-256 hashes of the chosen options — can be resolved back to option text.
type PollRepo struct {
	db dbExecQuerier
}

// NewPollRepo constructs a PollRepo.
func NewPollRepo(db dbExecQuerier) *PollRepo { return &PollRepo{db: db} }

// Upsert records (or refreshes) a poll's options, keyed by session + poll
// message id. Re-receiving the same poll creation is a no-op on the immutable
// fields and just bumps updated_at.
func (r *PollRepo) Upsert(ctx context.Context, p domain.Poll) error {
	options, err := json.Marshal(p.Options)
	if err != nil {
		return fmt.Errorf("store: marshal poll options: %w", err)
	}
	const q = `INSERT INTO polls
(session_id, poll_message_id, chat_jid, name, options, selectable_count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	chat_jid         = VALUES(chat_jid),
	name             = VALUES(name),
	options          = VALUES(options),
	selectable_count = VALUES(selectable_count),
	updated_at       = VALUES(updated_at)`
	if _, err := r.db.ExecContext(ctx, q,
		p.SessionID, p.PollMessageID, p.ChatJID, p.Name, options,
		p.SelectableCount, p.CreatedAt, p.UpdatedAt,
	); err != nil {
		return fmt.Errorf("store: upsert poll: %w", err)
	}
	return nil
}

// GetOptions returns a poll's options in creation order, or (nil, nil) when the
// poll is unknown (e.g. created before this session saw it). Callers treat the
// empty result as "cannot resolve" and fall back to the raw vote hashes.
func (r *PollRepo) GetOptions(ctx context.Context, sessionID, pollMessageID string) ([]string, error) {
	const q = `SELECT options FROM polls WHERE session_id = ? AND poll_message_id = ?`
	var raw []byte
	err := r.db.QueryRowContext(ctx, q, sessionID, pollMessageID).Scan(&raw)
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
