package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// GroupMemberRepo is the repository for whatsapp_group_members — the identity↔group
// pivot holding the per-group member tag and role (§5), keyed by
// (session_id, group_jid, lid).
type GroupMemberRepo struct {
	q *storedb.Queries
}

// NewGroupMemberRepo constructs a GroupMemberRepo.
func NewGroupMemberRepo(db storedb.DBTX) *GroupMemberRepo {
	return &GroupMemberRepo{q: storedb.New(db)}
}

func groupMemberFromRow(row storedb.WhatsappGroupMember) domain.GroupMember {
	return domain.GroupMember{
		ID:          row.ID,
		SessionID:   row.SessionID,
		GroupJID:    row.GroupJid,
		LID:         row.Lid,
		Tag:         stringPtrFromNull(row.Tag),
		Role:        domain.GroupRole(row.Role),
		FirstSeenAt: row.FirstSeenAt,
		LastSeenAt:  row.LastSeenAt,
	}
}

// Upsert inserts or updates a membership by (session_id, group_jid, lid). The
// tag refreshes only when non-NULL (COALESCE); role and last_seen_at always take
// the new value; first_seen_at is preserved.
func (r *GroupMemberRepo) Upsert(ctx context.Context, m domain.GroupMember) error {
	err := r.q.UpsertGroupMember(ctx, storedb.UpsertGroupMemberParams{
		SessionID:   m.SessionID,
		GroupJid:    m.GroupJID,
		Lid:         m.LID,
		Tag:         nullString(m.Tag),
		Role:        storedb.WhatsappGroupMembersRole(m.Role),
		FirstSeenAt: m.FirstSeenAt,
		LastSeenAt:  m.LastSeenAt,
	})
	if err != nil {
		return fmt.Errorf("store: upsert group member: %w", err)
	}
	return nil
}

// ListByGroup returns all members of a group for a session (§11 GET
// /groups/{gid}/members), ordered by id.
func (r *GroupMemberRepo) ListByGroup(ctx context.Context, sessionID, groupJID string) ([]domain.GroupMember, error) {
	rows, err := r.q.ListGroupMembersByGroup(ctx, storedb.ListGroupMembersByGroupParams{
		SessionID: sessionID,
		GroupJid:  groupJID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: list group members: %w", err)
	}
	out := make([]domain.GroupMember, 0, len(rows))
	for _, row := range rows {
		out = append(out, groupMemberFromRow(row))
	}
	return out, nil
}

// ListByContact returns every group membership of a given lid for a session —
// powers the §11 GET /contacts/{lid} "groups:[...]" view. Ordered by id.
func (r *GroupMemberRepo) ListByContact(ctx context.Context, sessionID, lid string) ([]domain.GroupMember, error) {
	rows, err := r.q.ListGroupMembersByContact(ctx, storedb.ListGroupMembersByContactParams{
		SessionID: sessionID,
		Lid:       lid,
	})
	if err != nil {
		return nil, fmt.Errorf("store: list memberships: %w", err)
	}
	out := make([]domain.GroupMember, 0, len(rows))
	for _, row := range rows {
		out = append(out, groupMemberFromRow(row))
	}
	return out, nil
}

// Remove deletes a membership (member removed from a group, §11 DELETE
// /groups/{gid}/members/{jid}).
func (r *GroupMemberRepo) Remove(ctx context.Context, sessionID, groupJID, lid string) error {
	n, err := r.q.RemoveGroupMember(ctx, storedb.RemoveGroupMemberParams{
		SessionID: sessionID,
		GroupJid:  groupJID,
		Lid:       lid,
	})
	if err != nil {
		return fmt.Errorf("store: remove group member: %w", err)
	}
	return rowsAffectedOrNotFound(n, "group member")
}
