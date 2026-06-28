package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// BackfillImportRepo is the repository for backfill_imports — the durable record
// of user-initiated WhatsApp backup imports. It backs both the job-status surface
// and the once-per-24h-per-session import quota.
type BackfillImportRepo struct {
	db dbExecQuerier
}

// NewBackfillImportRepo constructs a BackfillImportRepo.
func NewBackfillImportRepo(db dbExecQuerier) *BackfillImportRepo { return &BackfillImportRepo{db: db} }

const backfillImportCols = `id, session_id, organization_id, source, status, chats,
	messages, identities, groups_count, group_members, schema_fingerprint, error,
	created_at, finished_at`

func scanBackfillImport(s rowScanner) (domain.BackfillImport, error) {
	var (
		b           domain.BackfillImport
		errMsg      sql.NullString
		fingerprint sql.NullString
	)
	err := s.Scan(
		&b.ID, &b.SessionID, &b.OrganizationID, &b.Source, &b.Status, &b.Chats,
		&b.Messages, &b.Identities, &b.Groups, &b.GroupMembers, &fingerprint, &errMsg,
		&b.CreatedAt, &b.FinishedAt,
	)
	if err != nil {
		return domain.BackfillImport{}, err
	}
	b.Error = errMsg.String
	if fingerprint.Valid {
		b.SchemaFingerprint = &fingerprint.String
	}
	return b, nil
}

// Insert appends a new import row (typically in 'running' state).
func (r *BackfillImportRepo) Insert(ctx context.Context, b domain.BackfillImport) error {
	const q = `INSERT INTO backfill_imports
(id, session_id, organization_id, source, status, chats, messages, identities,
 groups_count, group_members, schema_fingerprint, error, created_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := r.db.ExecContext(ctx, q,
		b.ID, b.SessionID, b.OrganizationID, b.Source, b.Status, b.Chats, b.Messages,
		b.Identities, b.Groups, b.GroupMembers, b.SchemaFingerprint, nullableString(b.Error),
		b.CreatedAt, b.FinishedAt,
	); err != nil {
		return fmt.Errorf("store: insert backfill import: %w", err)
	}
	return nil
}

// Finish records a terminal (or progressed) state: status, counts, fingerprint,
// error and finished_at are written from the struct, keyed by id.
func (r *BackfillImportRepo) Finish(ctx context.Context, b domain.BackfillImport) error {
	const q = `UPDATE backfill_imports
SET status=?, chats=?, messages=?, identities=?, groups_count=?, group_members=?,
	schema_fingerprint=?, error=?, finished_at=?
WHERE id=?`
	res, err := r.db.ExecContext(ctx, q,
		b.Status, b.Chats, b.Messages, b.Identities, b.Groups, b.GroupMembers,
		b.SchemaFingerprint, nullableString(b.Error), b.FinishedAt, b.ID,
	)
	if err != nil {
		return fmt.Errorf("store: finish backfill import: %w", err)
	}
	return affectedOrNotFound(res, "backfill import")
}

// LatestForSession returns the most recent import for a session (the dashboard's
// status view). Maps no-rows to not_found.
func (r *BackfillImportRepo) LatestForSession(ctx context.Context, sessionID string) (domain.BackfillImport, error) {
	q := "SELECT " + backfillImportCols + " FROM backfill_imports WHERE session_id = ? ORDER BY created_at DESC LIMIT 1"
	b, err := scanBackfillImport(r.db.QueryRowContext(ctx, q, sessionID))
	if err != nil {
		return domain.BackfillImport{}, notFound(err, "backfill import")
	}
	return b, nil
}

// LastSuccessAt returns the created_at of the most recent succeeded import for a
// session, and whether one exists — the quota check (once per 24h for non-admins).
func (r *BackfillImportRepo) LastSuccessAt(ctx context.Context, sessionID string) (int64, bool, error) {
	const q = `SELECT created_at FROM backfill_imports
WHERE session_id = ? AND status = 'succeeded' ORDER BY created_at DESC LIMIT 1`
	var at int64
	err := r.db.QueryRowContext(ctx, q, sessionID).Scan(&at)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("store: last backfill success: %w", err)
	}
	return at, true, nil
}

// HasRunningSince reports whether a running import exists for the session created
// at/after sinceMs — the concurrency guard (a crashed job older than the window
// no longer blocks a retry).
func (r *BackfillImportRepo) HasRunningSince(ctx context.Context, sessionID string, sinceMs int64) (bool, error) {
	const q = `SELECT 1 FROM backfill_imports
WHERE session_id = ? AND status = 'running' AND created_at >= ? LIMIT 1`
	var one int
	err := r.db.QueryRowContext(ctx, q, sessionID, sinceMs).Scan(&one)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("store: running backfill check: %w", err)
	}
	return true, nil
}

// nullableString returns nil (→ SQL NULL) for an empty string, else the string.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
