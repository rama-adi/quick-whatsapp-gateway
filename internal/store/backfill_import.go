package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// BackfillImportRepo is the repository for backfill_imports — the durable record
// of user-initiated WhatsApp backup imports. It backs both the job-status surface
// and the once-per-24h-per-session import quota.
type BackfillImportRepo struct {
	q *storedb.Queries
}

// NewBackfillImportRepo constructs a BackfillImportRepo.
func NewBackfillImportRepo(db storedb.DBTX) *BackfillImportRepo {
	return &BackfillImportRepo{q: storedb.New(db)}
}

func backfillImportFromRow(row storedb.BackfillImport) domain.BackfillImport {
	b := domain.BackfillImport{
		ID:             row.ID,
		SessionID:      row.SessionID,
		OrganizationID: row.OrganizationID,
		Source:         row.Source,
		Status:         string(row.Status),
		Chats:          int(row.Chats),
		Messages:       int(row.Messages),
		Identities:     int(row.Identities),
		Groups:         int(row.GroupsCount),
		GroupMembers:   int(row.GroupMembers),
		Error:          row.Error.String,
		CreatedAt:      row.CreatedAt,
		FinishedAt:     int64PtrFromNull(row.FinishedAt),
	}
	if row.SchemaFingerprint.Valid {
		b.SchemaFingerprint = &row.SchemaFingerprint.String
	}
	return b
}

// Insert appends a new import row (typically in 'running' state).
func (r *BackfillImportRepo) Insert(ctx context.Context, b domain.BackfillImport) error {
	err := r.q.InsertBackfillImport(ctx, storedb.InsertBackfillImportParams{
		ID:                b.ID,
		SessionID:         b.SessionID,
		OrganizationID:    b.OrganizationID,
		Source:            b.Source,
		Status:            storedb.BackfillImportsStatus(b.Status),
		Chats:             int32(b.Chats),
		Messages:          int32(b.Messages),
		Identities:        int32(b.Identities),
		GroupsCount:       int32(b.Groups),
		GroupMembers:      int32(b.GroupMembers),
		SchemaFingerprint: nullString(b.SchemaFingerprint),
		Error:             nullStringFromValue(b.Error),
		CreatedAt:         b.CreatedAt,
		FinishedAt:        nullInt64(b.FinishedAt),
	})
	if err != nil {
		return fmt.Errorf("store: insert backfill import: %w", err)
	}
	return nil
}

// Finish records a terminal (or progressed) state: status, counts, fingerprint,
// error and finished_at are written from the struct, keyed by id.
func (r *BackfillImportRepo) Finish(ctx context.Context, b domain.BackfillImport) error {
	n, err := r.q.FinishBackfillImport(ctx, storedb.FinishBackfillImportParams{
		Status:            storedb.BackfillImportsStatus(b.Status),
		Chats:             int32(b.Chats),
		Messages:          int32(b.Messages),
		Identities:        int32(b.Identities),
		GroupsCount:       int32(b.Groups),
		GroupMembers:      int32(b.GroupMembers),
		SchemaFingerprint: nullString(b.SchemaFingerprint),
		Error:             nullStringFromValue(b.Error),
		FinishedAt:        nullInt64(b.FinishedAt),
		ID:                b.ID,
	})
	if err != nil {
		return fmt.Errorf("store: finish backfill import: %w", err)
	}
	return rowsAffectedOrNotFound(n, "backfill import")
}

// LatestForSession returns the most recent import for a session (the dashboard's
// status view). Maps no-rows to not_found.
func (r *BackfillImportRepo) LatestForSession(ctx context.Context, sessionID string) (domain.BackfillImport, error) {
	row, err := r.q.LatestBackfillImportForSession(ctx, storedb.LatestBackfillImportForSessionParams{SessionID: sessionID})
	if err != nil {
		return domain.BackfillImport{}, notFound(err, "backfill import")
	}
	return backfillImportFromRow(row), nil
}

// LastSuccessAt returns the created_at of the most recent succeeded import for a
// session, and whether one exists — the quota check (once per 24h for non-admins).
func (r *BackfillImportRepo) LastSuccessAt(ctx context.Context, sessionID string) (int64, bool, error) {
	at, err := r.q.LastSuccessfulBackfillImportCreatedAt(ctx, storedb.LastSuccessfulBackfillImportCreatedAtParams{SessionID: sessionID})
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
	running, err := r.q.RecentRunningBackfillImportExists(ctx, storedb.RecentRunningBackfillImportExistsParams{
		SessionID: sessionID,
		CreatedAt: sinceMs,
	})
	if err != nil {
		return false, fmt.Errorf("store: running backfill check: %w", err)
	}
	return running, nil
}
