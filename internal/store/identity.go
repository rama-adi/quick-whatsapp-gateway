package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// IdentityRepo is the repository for whatsapp_identities — global LID→phone/name
// resolution, upserted by lid on every inbound capture (§7).
type IdentityRepo struct {
	db dbExecQuerier
}

// NewIdentityRepo constructs an IdentityRepo.
func NewIdentityRepo(db dbExecQuerier) *IdentityRepo { return &IdentityRepo{db: db} }

const identityCols = `id, lid, phone_number, phone_jid, name, business_name,
	first_seen_at, updated_at`

func scanIdentity(s rowScanner) (domain.Identity, error) {
	var i domain.Identity
	err := s.Scan(
		&i.ID, &i.LID, &i.PhoneNumber, &i.PhoneJID, &i.Name, &i.BusinessName,
		&i.FirstSeenAt, &i.UpdatedAt,
	)
	if err != nil {
		return domain.Identity{}, err
	}
	return i, nil
}

// Upsert inserts or updates an identity by lid. On conflict the resolvable
// fields refresh but only when the new value is non-NULL (COALESCE keeps a
// previously-known phone/name if a later sighting lacks it); first_seen_at is
// preserved. This is the §7 "prefer push name / fill in resolution" behavior.
func (r *IdentityRepo) Upsert(ctx context.Context, i domain.Identity) error {
	const q = `INSERT INTO whatsapp_identities
(lid, phone_number, phone_jid, name, business_name, first_seen_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	phone_number  = COALESCE(VALUES(phone_number), phone_number),
	phone_jid     = COALESCE(VALUES(phone_jid), phone_jid),
	name          = COALESCE(VALUES(name), name),
	business_name = COALESCE(VALUES(business_name), business_name),
	updated_at    = VALUES(updated_at)`
	if _, err := r.db.ExecContext(ctx, q,
		i.LID, i.PhoneNumber, i.PhoneJID, i.Name, i.BusinessName, i.FirstSeenAt, i.UpdatedAt,
	); err != nil {
		return fmt.Errorf("store: upsert identity: %w", err)
	}
	return nil
}

// GetByLID fetches an identity by its unique lid. Maps no-rows to not_found.
func (r *IdentityRepo) GetByLID(ctx context.Context, lid string) (domain.Identity, error) {
	q := "SELECT " + identityCols + " FROM whatsapp_identities WHERE lid = ?"
	i, err := scanIdentity(r.db.QueryRowContext(ctx, q, lid))
	if err != nil {
		return domain.Identity{}, notFound(err, "identity")
	}
	return i, nil
}
