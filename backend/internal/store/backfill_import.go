package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
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

func backfillImportFromRow(row storedb.LatestBackfillImportForSessionRow) domain.BackfillImport {
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
		Owner:             sqlString(b.Owner),
		HeartbeatAt:       sql.NullInt64{Int64: b.CreatedAt, Valid: b.LeaseUntil > 0},
		LeaseUntil:        sql.NullInt64{Int64: b.LeaseUntil, Valid: b.LeaseUntil > 0},
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

// Finish records a terminal state: status, counts, fingerprint, error and
// finished_at are written from the struct. It is fenced by b.Owner and also
// overwrites 'interrupted', so a late real outcome is kept; not_found means
// another owner holds the row or it already finished.
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
		Owner:             sqlString(b.Owner),
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
	at, err := r.q.LastSuccessfulBackfillImportCreatedAt(ctx, storedb.LastSuccessfulBackfillImportCreatedAtParams{
		SessionID: sessionID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("store: last backfill success: %w", err)
	}
	return at, true, nil
}

// HasLiveRunning reports whether a running import with an unexpired lease
// exists for the session — the per-session concurrency guard.
func (r *BackfillImportRepo) HasLiveRunning(ctx context.Context, sessionID string, nowMs int64) (bool, error) {
	running, err := r.q.LiveRunningBackfillImportExists(ctx, storedb.LiveRunningBackfillImportExistsParams{
		SessionID: sessionID, LeaseUntil: sql.NullInt64{Int64: nowMs, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("store: running backfill check: %w", err)
	}
	return running, nil
}

// Heartbeat renews a running import's lease. ok=false means the row is no
// longer running under this owner (the reaper interrupted it).
func (r *BackfillImportRepo) Heartbeat(ctx context.Context, id, owner string, nowMs, leaseUntilMs int64) (bool, error) {
	n, err := r.q.HeartbeatBackfillImport(ctx, storedb.HeartbeatBackfillImportParams{
		HeartbeatAt: sql.NullInt64{Int64: nowMs, Valid: true},
		LeaseUntil:  sql.NullInt64{Int64: leaseUntilMs, Valid: true},
		ID:          id, Owner: sqlString(owner),
	})
	if err != nil {
		return false, fmt.Errorf("store: heartbeat backfill import: %w", err)
	}
	return n == 1, nil
}

// InterruptExpired marks every running import whose lease expired as
// interrupted and returns how many it ended.
func (r *BackfillImportRepo) InterruptExpired(ctx context.Context, nowMs int64) (int64, error) {
	n, err := r.q.InterruptExpiredBackfillImports(ctx, storedb.InterruptExpiredBackfillImportsParams{Now: sql.NullInt64{Int64: nowMs, Valid: true}})
	if err != nil {
		return 0, fmt.Errorf("store: interrupt expired backfill imports: %w", err)
	}
	return n, nil
}

// LiveIDs returns the ids of running imports with an unexpired lease.
func (r *BackfillImportRepo) LiveIDs(ctx context.Context, nowMs int64) ([]string, error) {
	ids, err := r.q.LiveBackfillImportIDs(ctx, storedb.LiveBackfillImportIDsParams{LeaseUntil: sql.NullInt64{Int64: nowMs, Valid: true}})
	if err != nil {
		return nil, fmt.Errorf("store: live backfill imports: %w", err)
	}
	return ids, nil
}
