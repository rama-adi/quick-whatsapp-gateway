package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Terminal engine-command statuses stored for idempotent replay. Only definite
// outcomes are persisted: a sent result, or a failure that occurred before any
// WhatsApp dispatch (validation/fencing). Transient and post-dispatch unknowns
// stay absent so an API retry re-issues the command instead of trusting a
// fabricated failure.
const (
	CommandSent   = "sent"
	CommandFailed = "failed"
)

// CommandResultRetention is the locked plan §17 default: command results are
// retained for seven days, covering the API's retry/idempotency window.
const CommandResultRetention = 7 * 24 * time.Hour

// errJournalUnavailable reports operations on a closed journal. The journal
// package has no closed sentinel; its other methods simply tolerate nil.
var errJournalUnavailable = errors.New("gateway journal is not open")

// CommandResult is one terminal outcome of a stable engine command.
type CommandResult struct {
	CommandID   string
	SessionID   string
	Status      string
	WAMessageID string
	Error       string
	UpdatedAt   time.Time
}

// LookupCommand returns the stored terminal result for a command id, or nil
// when no definite outcome exists yet.
func (j *Journal) LookupCommand(ctx context.Context, commandID string) (*CommandResult, error) {
	if j == nil || j.db == nil {
		return nil, errJournalUnavailable
	}
	if commandID == "" {
		return nil, fmt.Errorf("command id is required")
	}
	var (
		status      string
		sessionID   string
		waMessageID string
		errText     string
		updatedAtMs int64
	)
	err := j.db.QueryRowContext(ctx,
		`SELECT status, session_id, wa_message_id, error, updated_at FROM command_results WHERE command_id = ?`,
		commandID).Scan(&status, &sessionID, &waMessageID, &errText, &updatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, classifySQLiteError(fmt.Errorf("lookup command result: %w", err))
	}
	return &CommandResult{
		CommandID:   commandID,
		SessionID:   sessionID,
		Status:      status,
		WAMessageID: waMessageID,
		Error:       errText,
		UpdatedAt:   time.UnixMilli(updatedAtMs).UTC(),
	}, nil
}

// SaveCommandResult durably records a terminal outcome before its RPC response
// is written. A repeated save for the same command id keeps the first terminal
// result: results are immutable once recorded.
func (j *Journal) SaveCommandResult(ctx context.Context, result CommandResult) error {
	if j == nil || j.db == nil {
		return errJournalUnavailable
	}
	if result.CommandID == "" || result.SessionID == "" || result.UpdatedAt.IsZero() {
		return fmt.Errorf("command result id, session, and time are required")
	}
	if result.Status != CommandSent && result.Status != CommandFailed {
		return fmt.Errorf("command result status must be sent or failed")
	}
	expiresAt := result.UpdatedAt.Add(CommandResultRetention)
	_, err := j.db.ExecContext(ctx,
		`INSERT INTO command_results (command_id, session_id, status, wa_message_id, error, updated_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(command_id) DO NOTHING`,
		result.CommandID, result.SessionID, result.Status, result.WAMessageID, result.Error,
		result.UpdatedAt.UnixMilli(), expiresAt.UnixMilli())
	if err != nil {
		return classifySQLiteError(fmt.Errorf("save command result: %w", err))
	}
	return nil
}

// PruneCommands deletes expired command results. It runs opportunistically on
// the heartbeat metrics path; a failed prune only delays reuse of the space.
func (j *Journal) PruneCommands(ctx context.Context, now time.Time) error {
	if j == nil || j.db == nil {
		return errJournalUnavailable
	}
	_, err := j.db.ExecContext(ctx, `DELETE FROM command_results WHERE expires_at < ?`, now.UnixMilli())
	if err != nil {
		return classifySQLiteError(fmt.Errorf("prune command results: %w", err))
	}
	return nil
}
