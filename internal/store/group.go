package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// GroupRepo is the repository for whatsapp_groups (global group metadata, §5),
// upserted by group_jid.
type GroupRepo struct {
	db dbExecQuerier
}

// NewGroupRepo constructs a GroupRepo.
func NewGroupRepo(db dbExecQuerier) *GroupRepo { return &GroupRepo{db: db} }

const groupCols = `id, group_jid, subject, description, owner_jid,
	participant_count, is_announce, is_locked, created_at_wa, first_seen_at, updated_at`

func scanGroup(s rowScanner) (domain.Group, error) {
	var g domain.Group
	err := s.Scan(
		&g.ID, &g.GroupJID, &g.Subject, &g.Description, &g.OwnerJID,
		&g.ParticipantCount, &g.IsAnnounce, &g.IsLocked, &g.CreatedAtWA,
		&g.FirstSeenAt, &g.UpdatedAt,
	)
	if err != nil {
		return domain.Group{}, err
	}
	return g, nil
}

// Upsert inserts or updates a group by group_jid. Mutable metadata refreshes
// only when the new value is non-NULL (COALESCE) so a sparse sighting doesn't
// wipe known fields; first_seen_at is preserved.
func (r *GroupRepo) Upsert(ctx context.Context, g domain.Group) error {
	const q = `INSERT INTO whatsapp_groups
(group_jid, subject, description, owner_jid, participant_count, is_announce,
 is_locked, created_at_wa, first_seen_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	subject           = COALESCE(VALUES(subject), subject),
	description       = COALESCE(VALUES(description), description),
	owner_jid         = COALESCE(VALUES(owner_jid), owner_jid),
	participant_count = COALESCE(VALUES(participant_count), participant_count),
	is_announce       = COALESCE(VALUES(is_announce), is_announce),
	is_locked         = COALESCE(VALUES(is_locked), is_locked),
	created_at_wa     = COALESCE(VALUES(created_at_wa), created_at_wa),
	updated_at        = VALUES(updated_at)`
	if _, err := r.db.ExecContext(ctx, q,
		g.GroupJID, g.Subject, g.Description, g.OwnerJID, g.ParticipantCount,
		g.IsAnnounce, g.IsLocked, g.CreatedAtWA, g.FirstSeenAt, g.UpdatedAt,
	); err != nil {
		return fmt.Errorf("store: upsert group: %w", err)
	}
	return nil
}

// GetByJID fetches a group by its unique group_jid. Maps no-rows to not_found.
func (r *GroupRepo) GetByJID(ctx context.Context, groupJID string) (domain.Group, error) {
	q := "SELECT " + groupCols + " FROM whatsapp_groups WHERE group_jid = ?"
	g, err := scanGroup(r.db.QueryRowContext(ctx, q, groupJID))
	if err != nil {
		return domain.Group{}, notFound(err, "group")
	}
	return g, nil
}

// ListBySession returns the groups a session has membership sightings for (§11
// GET /groups). whatsapp_groups is global metadata, so this joins through the
// per-session group_members pivot to scope the result to the session. Ordered by
// group id for stability.
func (r *GroupRepo) ListBySession(ctx context.Context, sessionID string) ([]domain.Group, error) {
	q := "SELECT " + prefixCols("g", groupCols) + ` FROM whatsapp_groups g
		WHERE g.group_jid IN (
			SELECT DISTINCT group_jid FROM whatsapp_group_members WHERE session_id = ?
		)
		ORDER BY g.id ASC`
	rows, err := r.db.QueryContext(ctx, q, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list groups: %w", err)
	}
	defer rows.Close()
	var out []domain.Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
