package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// APIKeyRepo is the repository for api_keys (account-global keys, §5/§10).
type APIKeyRepo struct {
	db dbExecQuerier
}

// NewAPIKeyRepo constructs an APIKeyRepo.
func NewAPIKeyRepo(db dbExecQuerier) *APIKeyRepo { return &APIKeyRepo{db: db} }

const apiKeyCols = `id, tenant_id, name, key_prefix, key_hash, scope, permissions,
	last_used_at, expires_at, revoked_at, created_at`

func scanAPIKey(s rowScanner) (domain.APIKey, error) {
	var (
		k     domain.APIKey
		perms []byte
	)
	err := s.Scan(
		&k.ID, &k.TenantID, &k.Name, &k.KeyPrefix, &k.KeyHash, &k.Scope, &perms,
		&k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt, &k.CreatedAt,
	)
	if err != nil {
		return domain.APIKey{}, err
	}
	if len(perms) > 0 {
		if err := json.Unmarshal(perms, &k.Permissions); err != nil {
			return domain.APIKey{}, scanErr("api_keys.permissions", err)
		}
	}
	return k, nil
}

// Create inserts a new API key. The full key is never stored — only the prefix
// (for UI display) and the argon2id hash (in KeyHash) computed by the caller.
func (r *APIKeyRepo) Create(ctx context.Context, k domain.APIKey) error {
	perms, err := json.Marshal(k.Permissions)
	if err != nil {
		return fmt.Errorf("store: marshal permissions: %w", err)
	}
	const q = `INSERT INTO api_keys
(id, tenant_id, name, key_prefix, key_hash, scope, permissions, last_used_at,
 expires_at, revoked_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := r.db.ExecContext(ctx, q,
		k.ID, k.TenantID, k.Name, k.KeyPrefix, k.KeyHash, k.Scope, perms,
		k.LastUsedAt, k.ExpiresAt, k.RevokedAt, k.CreatedAt,
	); err != nil {
		return fmt.Errorf("store: create api key: %w", err)
	}
	return nil
}

// Get fetches a key by id. Maps no-rows to not_found.
func (r *APIKeyRepo) Get(ctx context.Context, id string) (domain.APIKey, error) {
	q := "SELECT " + apiKeyCols + " FROM api_keys WHERE id = ?"
	k, err := scanAPIKey(r.db.QueryRowContext(ctx, q, id))
	if err != nil {
		return domain.APIKey{}, notFound(err, "api key")
	}
	return k, nil
}

// GetByPrefix fetches a key by its unique prefix — the auth hot path: the prefix
// is parsed from the presented key, then KeyHash is verified by the caller.
func (r *APIKeyRepo) GetByPrefix(ctx context.Context, prefix string) (domain.APIKey, error) {
	q := "SELECT " + apiKeyCols + " FROM api_keys WHERE key_prefix = ?"
	k, err := scanAPIKey(r.db.QueryRowContext(ctx, q, prefix))
	if err != nil {
		return domain.APIKey{}, notFound(err, "api key")
	}
	return k, nil
}

// ListByTenant returns all keys for a tenant ordered by created_at desc.
func (r *APIKeyRepo) ListByTenant(ctx context.Context, tenantID string) ([]domain.APIKey, error) {
	q := "SELECT " + apiKeyCols + " FROM api_keys WHERE tenant_id = ? ORDER BY created_at DESC"
	rows, err := r.db.QueryContext(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: list api keys: %w", err)
	}
	defer rows.Close()
	var out []domain.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// UpdateLastUsed stamps last_used_at on a key (recorded on each authenticated
// request). Best-effort: a missing key is not an error here.
func (r *APIKeyRepo) UpdateLastUsed(ctx context.Context, id string, at int64) error {
	const q = "UPDATE api_keys SET last_used_at=? WHERE id=?"
	if _, err := r.db.ExecContext(ctx, q, at, id); err != nil {
		return fmt.Errorf("store: update api key last_used: %w", err)
	}
	return nil
}

// Revoke marks a key revoked at the given time. Idempotent-friendly: re-revoking
// just overwrites revoked_at.
func (r *APIKeyRepo) Revoke(ctx context.Context, id string, at int64) error {
	const q = "UPDATE api_keys SET revoked_at=? WHERE id=?"
	res, err := r.db.ExecContext(ctx, q, at, id)
	if err != nil {
		return fmt.Errorf("store: revoke api key: %w", err)
	}
	return affectedOrNotFound(res, "api key")
}

// Rotate replaces a key's secret in place: new prefix + new argon2id hash,
// clearing any prior revocation and refreshing last_used_at to NULL. The id is
// preserved so references (and the UI row) stay stable (§10 "rotatable").
func (r *APIKeyRepo) Rotate(ctx context.Context, id, newPrefix, newHash string) error {
	const q = `UPDATE api_keys SET key_prefix=?, key_hash=?, revoked_at=NULL, last_used_at=NULL WHERE id=?`
	res, err := r.db.ExecContext(ctx, q, newPrefix, newHash, id)
	if err != nil {
		return fmt.Errorf("store: rotate api key: %w", err)
	}
	return affectedOrNotFound(res, "api key")
}
