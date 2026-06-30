package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// GroupRepo is the repository for whatsapp_groups (global group metadata, §5),
// upserted by group_jid.
type GroupRepo struct {
	q *storedb.Queries
}

// NewGroupRepo constructs a GroupRepo.
func NewGroupRepo(db storedb.DBTX) *GroupRepo { return &GroupRepo{q: storedb.New(db)} }

func groupFromRow(row storedb.WhatsappGroup) domain.Group {
	return domain.Group{
		ID:               row.ID,
		GroupJID:         row.GroupJid,
		Subject:          stringPtrFromNull(row.Subject),
		Description:      stringPtrFromNull(row.Description),
		OwnerJID:         stringPtrFromNull(row.OwnerJid),
		ParticipantCount: intPtrFromNull32(row.ParticipantCount),
		IsAnnounce:       boolPtrFromNull(row.IsAnnounce),
		IsLocked:         boolPtrFromNull(row.IsLocked),
		CreatedAtWA:      int64PtrFromNull(row.CreatedAtWa),
		FirstSeenAt:      row.FirstSeenAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

// Upsert inserts or updates a group by group_jid. Mutable metadata refreshes
// only when the new value is non-NULL (COALESCE) so a sparse sighting doesn't
// wipe known fields; first_seen_at is preserved.
func (r *GroupRepo) Upsert(ctx context.Context, g domain.Group) error {
	err := r.q.UpsertGroup(ctx, storedb.UpsertGroupParams{
		GroupJid:         g.GroupJID,
		Subject:          nullString(g.Subject),
		Description:      nullString(g.Description),
		OwnerJid:         nullString(g.OwnerJID),
		ParticipantCount: nullInt32(g.ParticipantCount),
		IsAnnounce:       nullBool(g.IsAnnounce),
		IsLocked:         nullBool(g.IsLocked),
		CreatedAtWa:      nullInt64(g.CreatedAtWA),
		FirstSeenAt:      g.FirstSeenAt,
		UpdatedAt:        g.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("store: upsert group: %w", err)
	}
	return nil
}

// GetByJID fetches a group by its unique group_jid. Maps no-rows to not_found.
func (r *GroupRepo) GetByJID(ctx context.Context, groupJID string) (domain.Group, error) {
	row, err := r.q.GetGroupByJID(ctx, storedb.GetGroupByJIDParams{GroupJid: groupJID})
	if err != nil {
		return domain.Group{}, notFound(err, "group")
	}
	return groupFromRow(row), nil
}

// ListBySession returns the groups a session has membership sightings for (§11
// GET /groups). whatsapp_groups is global metadata, so this joins through the
// per-session group_members pivot to scope the result to the session. Ordered by
// group id for stability.
func (r *GroupRepo) ListBySession(ctx context.Context, sessionID string) ([]domain.Group, error) {
	rows, err := r.q.ListGroupsBySession(ctx, storedb.ListGroupsBySessionParams{SessionID: sessionID})
	if err != nil {
		return nil, fmt.Errorf("store: list groups: %w", err)
	}
	out := make([]domain.Group, 0, len(rows))
	for _, row := range rows {
		out = append(out, groupFromRow(row))
	}
	return out, nil
}
