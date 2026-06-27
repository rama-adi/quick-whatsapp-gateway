package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// GroupMemberRepo is the repository for whatsapp_group_members — the identity↔group
// pivot holding the per-group member tag and role (§5), keyed by
// (session_id, group_jid, lid).
type GroupMemberRepo struct {
	db dbExecQuerier
}

// NewGroupMemberRepo constructs a GroupMemberRepo.
func NewGroupMemberRepo(db dbExecQuerier) *GroupMemberRepo { return &GroupMemberRepo{db: db} }

const groupMemberCols = `id, session_id, group_jid, lid, tag, role,
	first_seen_at, last_seen_at`

func scanGroupMember(s rowScanner) (domain.GroupMember, error) {
	var m domain.GroupMember
	err := s.Scan(
		&m.ID, &m.SessionID, &m.GroupJID, &m.LID, &m.Tag, &m.Role,
		&m.FirstSeenAt, &m.LastSeenAt,
	)
	if err != nil {
		return domain.GroupMember{}, err
	}
	return m, nil
}

// Upsert inserts or updates a membership by (session_id, group_jid, lid). The
// tag refreshes only when non-NULL (COALESCE); role and last_seen_at always take
// the new value; first_seen_at is preserved.
func (r *GroupMemberRepo) Upsert(ctx context.Context, m domain.GroupMember) error {
	const q = `INSERT INTO whatsapp_group_members
(session_id, group_jid, lid, tag, role, first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	tag          = COALESCE(VALUES(tag), tag),
	role         = VALUES(role),
	last_seen_at = VALUES(last_seen_at)`
	if _, err := r.db.ExecContext(ctx, q,
		m.SessionID, m.GroupJID, m.LID, m.Tag, m.Role, m.FirstSeenAt, m.LastSeenAt,
	); err != nil {
		return fmt.Errorf("store: upsert group member: %w", err)
	}
	return nil
}

// ListByGroup returns all members of a group for a session (§11 GET
// /groups/{gid}/members), ordered by id.
func (r *GroupMemberRepo) ListByGroup(ctx context.Context, sessionID, groupJID string) ([]domain.GroupMember, error) {
	q := "SELECT " + groupMemberCols + " FROM whatsapp_group_members WHERE session_id = ? AND group_jid = ? ORDER BY id ASC"
	rows, err := r.db.QueryContext(ctx, q, sessionID, groupJID)
	if err != nil {
		return nil, fmt.Errorf("store: list group members: %w", err)
	}
	defer rows.Close()
	var out []domain.GroupMember
	for rows.Next() {
		m, err := scanGroupMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListByContact returns every group membership of a given lid for a session —
// powers the §11 GET /contacts/{lid} "groups:[...]" view. Ordered by id.
func (r *GroupMemberRepo) ListByContact(ctx context.Context, sessionID, lid string) ([]domain.GroupMember, error) {
	q := "SELECT " + groupMemberCols + " FROM whatsapp_group_members WHERE session_id = ? AND lid = ? ORDER BY id ASC"
	rows, err := r.db.QueryContext(ctx, q, sessionID, lid)
	if err != nil {
		return nil, fmt.Errorf("store: list memberships: %w", err)
	}
	defer rows.Close()
	var out []domain.GroupMember
	for rows.Next() {
		m, err := scanGroupMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Remove deletes a membership (member removed from a group, §11 DELETE
// /groups/{gid}/members/{jid}).
func (r *GroupMemberRepo) Remove(ctx context.Context, sessionID, groupJID, lid string) error {
	const q = "DELETE FROM whatsapp_group_members WHERE session_id = ? AND group_jid = ? AND lid = ?"
	res, err := r.db.ExecContext(ctx, q, sessionID, groupJID, lid)
	if err != nil {
		return fmt.Errorf("store: remove group member: %w", err)
	}
	return affectedOrNotFound(res, "group member")
}
