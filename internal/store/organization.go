package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// OrganizationReader is a READ-ONLY view of better-auth's `organization` table
// (shared MySQL). better-auth's organization plugin is the sole writer; the
// gateway only ever reads to enforce the boot orphan-guard (§4.6 boot
// reconciliation, §17 R2): a session whose owning org was deleted while this
// gateway was down must not be resumed.
//
// ASSUMPTION (R5 contract test must confirm against the pinned better-auth
// version): the organization table is literally named `organization` and its
// primary key column is `id`. better-auth's organization plugin uses a camelCase
// schema; existence is checked with `SELECT 1 FROM organization WHERE id = ?`.
// There is no `enabled`/`disabled` column in better-auth's default organization
// schema, so "enabled" collapses to "exists" here — if a later better-auth
// version adds a soft-disable flag, extend the predicate.
type OrganizationReader struct {
	q *storedb.Queries
}

// NewOrganizationReader constructs an OrganizationReader over the shared DB.
func NewOrganizationReader(db storedb.DBTX) *OrganizationReader {
	return &OrganizationReader{q: storedb.New(db)}
}

// Exists reports whether an organization id is present in better-auth's
// `organization` table. A non-existent id returns (false, nil); a real query
// failure returns (false, err) so callers can fail safe rather than treating a
// DB blip as "org gone".
func (r *OrganizationReader) Exists(ctx context.Context, organizationID string) (bool, error) {
	if organizationID == "" {
		return false, nil
	}
	ok, err := r.q.OrganizationExists(ctx, storedb.OrganizationExistsParams{ID: organizationID})
	if err != nil {
		return false, fmt.Errorf("store: check organization exists: %w", err)
	}
	return ok, nil
}
