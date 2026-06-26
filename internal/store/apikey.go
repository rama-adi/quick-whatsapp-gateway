package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// APIKeyRepo is a READ-ONLY view of better-auth's `apikey` table (§4.2/§7). The
// frontend's better-auth api-key plugin is the sole writer; the gateway only ever
// reads a row to verify a presented key and resolve its owning organization. There
// is intentionally no Create/Update/Revoke/Rotate here — those live in the
// frontend.
//
// Hashing note: better-auth stores keys hashed (column `key`) and hashes presented
// keys deterministically by default, so a key is verified by hashing the presented
// secret and looking the row up by that hash. This repo does NOT hash — the
// authz package (Stage 3) supplies the hash and calls GetByHash. That keeps the
// hashing scheme (which must match better-auth's pinned version) in one place.
type APIKeyRepo struct {
	db dbExecQuerier
}

// NewAPIKeyRepo constructs an APIKeyRepo.
func NewAPIKeyRepo(db dbExecQuerier) *APIKeyRepo { return &APIKeyRepo{db: db} }

// apiKeyCols maps better-auth's apikey columns onto domain.APIKey. The `key`
// column holds the stored hash (KeyHash); refillInterval/rateLimit/metadata are
// ignored for now (§4.2). organizationId is nullable in better-auth (a key may be
// user-scoped); scanAPIKey falls back to userId-derived ownership upstream.
const apiKeyCols = "id, name, `key`, userId, organizationId, enabled, expiresAt, permissions, createdAt"

func scanAPIKey(s rowScanner) (domain.APIKey, error) {
	var (
		k     domain.APIKey
		orgID *string
		perms []byte
	)
	err := s.Scan(
		&k.ID, &k.Name, &k.KeyHash, &k.UserID, &orgID, &k.Enabled, &k.ExpiresAt, &perms, &k.CreatedAt,
	)
	if err != nil {
		return domain.APIKey{}, err
	}
	if orgID != nil {
		k.OrganizationID = *orgID
	}
	if len(perms) > 0 {
		if err := json.Unmarshal(perms, &k.Permissions); err != nil {
			return domain.APIKey{}, scanErr("apikey.permissions", err)
		}
	}
	return k, nil
}

// GetByHash fetches a key by its stored hash (better-auth's `key` column) — the
// auth hot path. The caller (authz) hashes the presented secret with better-auth's
// scheme and passes the digest here. Maps no-rows to not_found.
func (r *APIKeyRepo) GetByHash(ctx context.Context, keyHash string) (domain.APIKey, error) {
	q := "SELECT " + apiKeyCols + " FROM apikey WHERE `key` = ?"
	k, err := scanAPIKey(r.db.QueryRowContext(ctx, q, keyHash))
	if err != nil {
		return domain.APIKey{}, notFound(err, "api key")
	}
	return k, nil
}

// GetByID fetches a key by id. Maps no-rows to not_found.
func (r *APIKeyRepo) GetByID(ctx context.Context, id string) (domain.APIKey, error) {
	q := "SELECT " + apiKeyCols + " FROM apikey WHERE id = ?"
	k, err := scanAPIKey(r.db.QueryRowContext(ctx, q, id))
	if err != nil {
		return domain.APIKey{}, notFound(err, "api key")
	}
	return k, nil
}

// TouchLastRequest best-effort stamps better-auth's `lastRequest` column on use.
// A missing row is not an error here. The frontend owns the column; this is a
// courtesy write so the dashboard's "last used" reflects gateway traffic.
func (r *APIKeyRepo) TouchLastRequest(ctx context.Context, id string, at int64) error {
	const q = "UPDATE apikey SET lastRequest=? WHERE id=?"
	if _, err := r.db.ExecContext(ctx, q, at, id); err != nil {
		return fmt.Errorf("store: touch apikey lastRequest: %w", err)
	}
	return nil
}
