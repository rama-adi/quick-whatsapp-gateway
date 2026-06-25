package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// TenantRepo is the repository for the tenants table — a thin app-side mirror of
// an Authula user keyed by the Authula user id (§5).
type TenantRepo struct {
	db dbExecQuerier
}

// NewTenantRepo constructs a TenantRepo over the given handle.
func NewTenantRepo(db dbExecQuerier) *TenantRepo { return &TenantRepo{db: db} }

const tenantCols = "id, email, display_name, created_at, updated_at"

func scanTenant(s rowScanner) (domain.Tenant, error) {
	var t domain.Tenant
	if err := s.Scan(&t.ID, &t.Email, &t.DisplayName, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return domain.Tenant{}, err
	}
	return t, nil
}

// Upsert inserts or updates a tenant by id (the Authula user id). created_at is
// preserved on update; updated_at and the mutable fields are refreshed.
func (r *TenantRepo) Upsert(ctx context.Context, t domain.Tenant) error {
	const q = `INSERT INTO tenants (id, email, display_name, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE email=VALUES(email), display_name=VALUES(display_name), updated_at=VALUES(updated_at)`
	if _, err := r.db.ExecContext(ctx, q, t.ID, t.Email, t.DisplayName, t.CreatedAt, t.UpdatedAt); err != nil {
		return fmt.Errorf("store: upsert tenant: %w", err)
	}
	return nil
}

// GetByID fetches a tenant by id. Maps no-rows to not_found.
func (r *TenantRepo) GetByID(ctx context.Context, id string) (domain.Tenant, error) {
	q := "SELECT " + tenantCols + " FROM tenants WHERE id = ?"
	t, err := scanTenant(r.db.QueryRowContext(ctx, q, id))
	if err != nil {
		return domain.Tenant{}, notFound(err, "tenant")
	}
	return t, nil
}

// GetByEmail fetches a tenant by its unique email. Maps no-rows to not_found.
func (r *TenantRepo) GetByEmail(ctx context.Context, email string) (domain.Tenant, error) {
	q := "SELECT " + tenantCols + " FROM tenants WHERE email = ?"
	t, err := scanTenant(r.db.QueryRowContext(ctx, q, email))
	if err != nil {
		return domain.Tenant{}, notFound(err, "tenant")
	}
	return t, nil
}
