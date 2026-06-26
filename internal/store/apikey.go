package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

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

// apiKeyCols maps better-auth's ACTUAL apikey columns (better-auth 1.6.x, Drizzle
// MySQL adapter — verified live, see contract test) onto domain.APIKey. The `key`
// column holds the stored hash (KeyHash). Column names are snake_case and there is
// NO `organizationId`/`userId` column: because the api-key plugin is configured
// with `references: "organization"`, the OWNING ORG id lives in `reference_id`.
// `expires_at`/`created_at` are MySQL TIMESTAMP(3) (DATETIME), NOT epoch-ms BIGINT,
// so they scan as time and convert to epoch-ms for the domain. refill/rateLimit/
// metadata columns are ignored for now (§4.2).
const apiKeyCols = "id, name, `key`, reference_id, enabled, expires_at, permissions, created_at"

func scanAPIKey(s rowScanner) (domain.APIKey, error) {
	var (
		k         domain.APIKey
		refID     sql.NullString
		enabled   sql.NullBool
		expiresAt sql.NullTime
		createdAt sql.NullTime
		perms     []byte
	)
	err := s.Scan(
		&k.ID, &k.Name, &k.KeyHash, &refID, &enabled, &expiresAt, &perms, &createdAt,
	)
	if err != nil {
		return domain.APIKey{}, err
	}
	// reference_id == the owning organization id (plugin `references:"organization"`).
	if refID.Valid {
		k.OrganizationID = refID.String
	}
	// better-auth's `enabled` is nullable with default true; treat NULL as enabled.
	k.Enabled = !enabled.Valid || enabled.Bool
	if expiresAt.Valid {
		ms := expiresAt.Time.UnixMilli()
		k.ExpiresAt = &ms
	}
	if createdAt.Valid {
		k.CreatedAt = createdAt.Time.UnixMilli()
	}
	if len(perms) > 0 {
		p, err := parseAPIKeyPermissions(perms)
		if err != nil {
			return domain.APIKey{}, scanErr("apikey.permissions", err)
		}
		k.Permissions = p
	}
	return k, nil
}

// parseAPIKeyPermissions decodes better-auth's api-key `permissions` JSON into the
// gateway's flag set. better-auth stores a resource->actions map, e.g.
//
//	{"gateway":["read","send","manage","events"]}
//
// (see the apiKey plugin `permissions` config in web/app/lib/auth/server.ts: the
// {read,send,manage,events} scopes hang off a single "gateway" resource bucket).
// Actions are matched case-insensitively under the "gateway" key; unknown
// resources/actions are ignored.
func parseAPIKeyPermissions(raw []byte) (domain.Permissions, error) {
	var m map[string][]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return domain.Permissions{}, err
	}
	var p domain.Permissions
	for _, action := range m["gateway"] {
		switch action {
		case "read":
			p.Read = true
		case "send":
			p.Send = true
		case "manage":
			p.Manage = true
		case "events":
			p.Events = true
		}
	}
	return p, nil
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

// TouchLastRequest best-effort stamps better-auth's `last_request` column on use.
// `at` is epoch-ms; the column is a MySQL TIMESTAMP(3), so we pass a time.Time.
// A missing row is not an error here. The frontend owns the column; this is a
// courtesy write so the dashboard's "last used" reflects gateway traffic.
func (r *APIKeyRepo) TouchLastRequest(ctx context.Context, id string, at int64) error {
	const q = "UPDATE apikey SET last_request=? WHERE id=?"
	if _, err := r.db.ExecContext(ctx, q, time.UnixMilli(at).UTC(), id); err != nil {
		return fmt.Errorf("store: touch apikey last_request: %w", err)
	}
	return nil
}
