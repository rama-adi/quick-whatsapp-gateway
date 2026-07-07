package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

type OAuthClientRepo struct{ db dbExecQuerier }
type OAuthGrantRepo struct{ db dbExecQuerier }
type OAuthRefreshTokenRepo struct{ db dbExecQuerier }
type OAuthSigningKeyRepo struct{ db dbExecQuerier }

func NewOAuthClientRepo(db dbExecQuerier) *OAuthClientRepo { return &OAuthClientRepo{db: db} }
func NewOAuthGrantRepo(db dbExecQuerier) *OAuthGrantRepo   { return &OAuthGrantRepo{db: db} }
func NewOAuthRefreshTokenRepo(db dbExecQuerier) *OAuthRefreshTokenRepo {
	return &OAuthRefreshTokenRepo{db: db}
}
func NewOAuthSigningKeyRepo(db dbExecQuerier) *OAuthSigningKeyRepo {
	return &OAuthSigningKeyRepo{db: db}
}

const oauthClientCols = `id, client_id, organization_id, created_by_user_id, session_id, name, logo_url, client_type, login_command, secret_hash, secret_last4, redirect_uris, modes, group_jid, allowed_scopes, token_ttl_seconds, refresh_ttl_seconds, status, created_at, updated_at, deleted_at`

func scanOAuthClient(s rowScanner) (domain.OAuthClient, error) {
	var c domain.OAuthClient
	var createdBy, logo, secretLast4, group sql.NullString
	var deleted sql.NullInt64
	if err := s.Scan(&c.ID, &c.ClientID, &c.OrganizationID, &createdBy, &c.SessionID, &c.Name, &logo, &c.ClientType, &c.LoginCommand, &c.SecretHash, &secretLast4, &c.RedirectURIs, &c.Modes, &group, &c.AllowedScopes, &c.TokenTTLSeconds, &c.RefreshTTLSeconds, &c.Status, &c.CreatedAt, &c.UpdatedAt, &deleted); err != nil {
		return domain.OAuthClient{}, scanErr("oauth_clients", err)
	}
	c.CreatedByUserID, c.LogoURL, c.SecretLast4, c.GroupJID = stringPtrFromNull(createdBy), stringPtrFromNull(logo), stringPtrFromNull(secretLast4), stringPtrFromNull(group)
	c.DeletedAt = int64PtrFromNull(deleted)
	return c, nil
}

func (r *OAuthClientRepo) Create(ctx context.Context, c domain.OAuthClient) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO oauth_clients (`+oauthClientCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ClientID, c.OrganizationID, nullString(c.CreatedByUserID), c.SessionID, c.Name, nullString(c.LogoURL), c.ClientType, c.LoginCommand, c.SecretHash, nullString(c.SecretLast4), c.RedirectURIs, c.Modes, nullString(c.GroupJID), c.AllowedScopes, c.TokenTTLSeconds, c.RefreshTTLSeconds, c.Status, c.CreatedAt, c.UpdatedAt, nullInt64(c.DeletedAt))
	if err != nil {
		return fmt.Errorf("store: create oauth client: %w", err)
	}
	return nil
}

func (r *OAuthClientRepo) GetByOrg(ctx context.Context, orgID, id string) (domain.OAuthClient, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+oauthClientCols+` FROM oauth_clients WHERE organization_id = ? AND id = ? AND deleted_at IS NULL`, orgID, id)
	c, err := scanOAuthClient(row)
	if err != nil {
		return domain.OAuthClient{}, notFound(err, "oauth client")
	}
	return c, nil
}

func (r *OAuthClientRepo) GetAny(ctx context.Context, id string) (domain.OAuthClient, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+oauthClientCols+` FROM oauth_clients WHERE id = ? AND deleted_at IS NULL`, id)
	c, err := scanOAuthClient(row)
	if err != nil {
		return domain.OAuthClient{}, notFound(err, "oauth client")
	}
	return c, nil
}

func (r *OAuthClientRepo) GetActiveByClientID(ctx context.Context, clientID string) (domain.OAuthClient, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+oauthClientCols+` FROM oauth_clients WHERE client_id = ? AND status = 'active' AND deleted_at IS NULL`, clientID)
	c, err := scanOAuthClient(row)
	if err != nil {
		return domain.OAuthClient{}, notFound(err, "oauth client")
	}
	return c, nil
}

func (r *OAuthClientRepo) ListByOrg(ctx context.Context, orgID, cursor string, limit int) (Page[domain.OAuthClient], error) {
	limit = normLimit(limit)
	cur, err := parseStringCursor(cursor)
	if err != nil {
		return Page[domain.OAuthClient]{}, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+oauthClientCols+` FROM oauth_clients WHERE organization_id = ? AND deleted_at IS NULL AND (? = '' OR id > ?) ORDER BY id ASC LIMIT ?`, orgID, cur, cur, limit)
	if err != nil {
		return Page[domain.OAuthClient]{}, fmt.Errorf("store: list oauth clients: %w", err)
	}
	defer rows.Close()
	items := make([]domain.OAuthClient, 0, limit)
	for rows.Next() {
		c, err := scanOAuthClient(rows)
		if err != nil {
			return Page[domain.OAuthClient]{}, err
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.OAuthClient]{}, fmt.Errorf("store: list oauth clients: %w", err)
	}
	return pageFromString(items, limit, func(c domain.OAuthClient) string { return c.ID }), nil
}

func (r *OAuthClientRepo) Update(ctx context.Context, c domain.OAuthClient) error {
	res, err := r.db.ExecContext(ctx, `UPDATE oauth_clients SET session_id = ?, name = ?, logo_url = ?, client_type = ?, login_command = ?, secret_hash = ?, secret_last4 = ?, redirect_uris = ?, modes = ?, group_jid = ?, allowed_scopes = ?, token_ttl_seconds = ?, refresh_ttl_seconds = ?, status = ?, updated_at = ? WHERE organization_id = ? AND id = ? AND deleted_at IS NULL`,
		c.SessionID, c.Name, nullString(c.LogoURL), c.ClientType, c.LoginCommand, c.SecretHash, nullString(c.SecretLast4), c.RedirectURIs, c.Modes, nullString(c.GroupJID), c.AllowedScopes, c.TokenTTLSeconds, c.RefreshTTLSeconds, c.Status, c.UpdatedAt, c.OrganizationID, c.ID)
	if err != nil {
		return fmt.Errorf("store: update oauth client: %w", err)
	}
	return affectedOrNotFound(res, "oauth client")
}

func (r *OAuthClientRepo) SoftDelete(ctx context.Context, orgID, id string, deletedAt int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE oauth_clients SET deleted_at = ?, updated_at = ? WHERE organization_id = ? AND id = ? AND deleted_at IS NULL`, deletedAt, deletedAt, orgID, id)
	if err != nil {
		return fmt.Errorf("store: delete oauth client: %w", err)
	}
	return affectedOrNotFound(res, "oauth client")
}

const oauthGrantCols = `id, organization_id, client_id, wa_identity_id, sub, granted_scopes, last_acr, last_group_jid, created_at, last_used_at, revoked_at`

func scanOAuthGrant(s rowScanner) (domain.OAuthGrant, error) {
	var g domain.OAuthGrant
	var lastGroup sql.NullString
	var revoked sql.NullInt64
	if err := s.Scan(&g.ID, &g.OrganizationID, &g.ClientID, &g.WAIdentityID, &g.Sub, &g.GrantedScopes, &g.LastACR, &lastGroup, &g.CreatedAt, &g.LastUsedAt, &revoked); err != nil {
		return domain.OAuthGrant{}, scanErr("oauth_grants", err)
	}
	g.LastGroupJID, g.RevokedAt = stringPtrFromNull(lastGroup), int64PtrFromNull(revoked)
	return g, nil
}

func (r *OAuthGrantRepo) Upsert(ctx context.Context, g domain.OAuthGrant) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO oauth_grants (`+oauthGrantCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE sub = VALUES(sub), granted_scopes = VALUES(granted_scopes), last_acr = VALUES(last_acr), last_group_jid = VALUES(last_group_jid), last_used_at = VALUES(last_used_at), revoked_at = NULL`,
		g.ID, g.OrganizationID, g.ClientID, g.WAIdentityID, g.Sub, g.GrantedScopes, g.LastACR, nullString(g.LastGroupJID), g.CreatedAt, g.LastUsedAt, nullInt64(g.RevokedAt))
	if err != nil {
		return fmt.Errorf("store: upsert oauth grant: %w", err)
	}
	return nil
}

func (r *OAuthGrantRepo) GetByOrg(ctx context.Context, orgID, id string) (domain.OAuthGrant, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants WHERE organization_id = ? AND id = ?`, orgID, id)
	g, err := scanOAuthGrant(row)
	if err != nil {
		return domain.OAuthGrant{}, notFound(err, "oauth grant")
	}
	return g, nil
}

func (r *OAuthGrantRepo) ListByClient(ctx context.Context, orgID, clientID, cursor string, limit int) (Page[domain.OAuthGrant], error) {
	limit = normLimit(limit)
	cur, err := parseStringCursor(cursor)
	if err != nil {
		return Page[domain.OAuthGrant]{}, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants WHERE organization_id = ? AND client_id = ? AND (? = '' OR id > ?) ORDER BY id ASC LIMIT ?`, orgID, clientID, cur, cur, limit)
	if err != nil {
		return Page[domain.OAuthGrant]{}, fmt.Errorf("store: list oauth grants: %w", err)
	}
	defer rows.Close()
	items := make([]domain.OAuthGrant, 0, limit)
	for rows.Next() {
		g, err := scanOAuthGrant(rows)
		if err != nil {
			return Page[domain.OAuthGrant]{}, err
		}
		items = append(items, g)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.OAuthGrant]{}, fmt.Errorf("store: list oauth grants: %w", err)
	}
	return pageFromString(items, limit, func(g domain.OAuthGrant) string { return g.ID }), nil
}

func (r *OAuthGrantRepo) RevokeByClient(ctx context.Context, orgID, clientID string, revokedAt int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE oauth_grants SET revoked_at = ? WHERE organization_id = ? AND client_id = ? AND revoked_at IS NULL`, revokedAt, orgID, clientID)
	if err != nil {
		return fmt.Errorf("store: revoke oauth grants by client: %w", err)
	}
	return nil
}

func (r *OAuthGrantRepo) GetActiveByClientIdentity(ctx context.Context, orgID, clientID string, identityID uint64) (domain.OAuthGrant, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants WHERE organization_id = ? AND client_id = ? AND wa_identity_id = ? AND revoked_at IS NULL`, orgID, clientID, identityID)
	g, err := scanOAuthGrant(row)
	if err != nil {
		return domain.OAuthGrant{}, notFound(err, "oauth grant")
	}
	return g, nil
}

func (r *OAuthGrantRepo) Revoke(ctx context.Context, orgID, id string, revokedAt int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE oauth_grants SET revoked_at = ? WHERE organization_id = ? AND id = ? AND revoked_at IS NULL`, revokedAt, orgID, id)
	if err != nil {
		return fmt.Errorf("store: revoke oauth grant: %w", err)
	}
	return affectedOrNotFound(res, "oauth grant")
}

const oauthRefreshTokenCols = `id, grant_id, organization_id, token_hash, family_id, parent_id, scopes, issued_at, expires_at, consumed_at, revoked_at`

func scanOAuthRefreshToken(s rowScanner) (domain.OAuthRefreshToken, error) {
	var rt domain.OAuthRefreshToken
	var parent sql.NullString
	var consumed, revoked sql.NullInt64
	if err := s.Scan(&rt.ID, &rt.GrantID, &rt.OrganizationID, &rt.TokenHash, &rt.FamilyID, &parent, &rt.Scopes, &rt.IssuedAt, &rt.ExpiresAt, &consumed, &revoked); err != nil {
		return domain.OAuthRefreshToken{}, scanErr("oauth_refresh_tokens", err)
	}
	rt.ParentID, rt.ConsumedAt, rt.RevokedAt = stringPtrFromNull(parent), int64PtrFromNull(consumed), int64PtrFromNull(revoked)
	return rt, nil
}

func (r *OAuthRefreshTokenRepo) Create(ctx context.Context, rt domain.OAuthRefreshToken) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens (`+oauthRefreshTokenCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rt.ID, rt.GrantID, rt.OrganizationID, rt.TokenHash, rt.FamilyID, nullString(rt.ParentID), rt.Scopes, rt.IssuedAt, rt.ExpiresAt, nullInt64(rt.ConsumedAt), nullInt64(rt.RevokedAt))
	if err != nil {
		return fmt.Errorf("store: create oauth refresh token: %w", err)
	}
	return nil
}

func (r *OAuthRefreshTokenRepo) GetByHash(ctx context.Context, tokenHash []byte) (domain.OAuthRefreshToken, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+oauthRefreshTokenCols+` FROM oauth_refresh_tokens WHERE token_hash = ?`, tokenHash)
	rt, err := scanOAuthRefreshToken(row)
	if err != nil {
		return domain.OAuthRefreshToken{}, notFound(err, "oauth refresh token")
	}
	return rt, nil
}

func (r *OAuthRefreshTokenRepo) MarkConsumed(ctx context.Context, id string, consumedAt int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL`, consumedAt, id)
	if err != nil {
		return fmt.Errorf("store: consume oauth refresh token: %w", err)
	}
	return affectedOrNotFound(res, "oauth refresh token")
}

func (r *OAuthRefreshTokenRepo) RevokeFamily(ctx context.Context, familyID string, revokedAt int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL`, revokedAt, familyID)
	if err != nil {
		return fmt.Errorf("store: revoke oauth refresh token family: %w", err)
	}
	return nil
}

func (r *OAuthRefreshTokenRepo) RevokeByGrant(ctx context.Context, orgID, grantID string, revokedAt int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET revoked_at = ? WHERE organization_id = ? AND grant_id = ? AND revoked_at IS NULL`, revokedAt, orgID, grantID)
	if err != nil {
		return fmt.Errorf("store: revoke oauth refresh tokens by grant: %w", err)
	}
	return nil
}

func (r *OAuthRefreshTokenRepo) RevokeByClient(ctx context.Context, orgID, clientID string, revokedAt int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE oauth_refresh_tokens rt JOIN oauth_grants g ON g.id = rt.grant_id SET rt.revoked_at = ? WHERE rt.organization_id = ? AND g.organization_id = ? AND g.client_id = ? AND rt.revoked_at IS NULL`, revokedAt, orgID, orgID, clientID)
	if err != nil {
		return fmt.Errorf("store: revoke oauth refresh tokens by client: %w", err)
	}
	return nil
}

const oauthSigningKeyCols = `kid, alg, public_jwk, private_enc, status, created_at, retired_at`

func scanOAuthSigningKey(s rowScanner) (domain.OAuthSigningKey, error) {
	var k domain.OAuthSigningKey
	var retired sql.NullInt64
	if err := s.Scan(&k.KID, &k.Alg, &k.PublicJWK, &k.PrivateEnc, &k.Status, &k.CreatedAt, &retired); err != nil {
		return domain.OAuthSigningKey{}, scanErr("oauth_signing_keys", err)
	}
	k.RetiredAt = int64PtrFromNull(retired)
	return k, nil
}

func (r *OAuthSigningKeyRepo) Create(ctx context.Context, k domain.OAuthSigningKey) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO oauth_signing_keys (`+oauthSigningKeyCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`, k.KID, k.Alg, k.PublicJWK, k.PrivateEnc, k.Status, k.CreatedAt, nullInt64(k.RetiredAt))
	if err != nil {
		return fmt.Errorf("store: create oauth signing key: %w", err)
	}
	return nil
}

func (r *OAuthSigningKeyRepo) GetActive(ctx context.Context) (domain.OAuthSigningKey, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+oauthSigningKeyCols+` FROM oauth_signing_keys WHERE status = 'active'`)
	k, err := scanOAuthSigningKey(row)
	if err != nil {
		return domain.OAuthSigningKey{}, notFound(err, "oauth signing key")
	}
	return k, nil
}

func (r *OAuthSigningKeyRepo) ListPublic(ctx context.Context) ([]domain.OAuthSigningKey, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+oauthSigningKeyCols+` FROM oauth_signing_keys WHERE status IN ('active', 'next', 'retired') ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list oauth signing keys: %w", err)
	}
	defer rows.Close()
	var out []domain.OAuthSigningKey
	for rows.Next() {
		k, err := scanOAuthSigningKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list oauth signing keys: %w", err)
	}
	return out, nil
}

func (r *OAuthSigningKeyRepo) CountByStatus(ctx context.Context, status string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_signing_keys WHERE status = ?`, status).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count oauth signing keys: %w", err)
	}
	return n, nil
}

func (r *OAuthSigningKeyRepo) PromoteNext(ctx context.Context, kid string, retiredAt int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE oauth_signing_keys SET status = 'retired', retired_at = ? WHERE status = 'active'`, retiredAt)
	if err != nil {
		return fmt.Errorf("store: retire active oauth signing key: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE oauth_signing_keys SET status = 'active' WHERE kid = ? AND status = 'next'`, kid)
	if err != nil {
		return fmt.Errorf("store: promote oauth signing key: %w", err)
	}
	return affectedOrNotFound(res, "oauth signing key")
}

func (r *OAuthSigningKeyRepo) Retire(ctx context.Context, kid string, retiredAt int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE oauth_signing_keys SET status = 'retired', retired_at = ? WHERE kid = ? AND status IN ('active', 'next')`, retiredAt, kid)
	if err != nil {
		return fmt.Errorf("store: retire oauth signing key: %w", err)
	}
	return affectedOrNotFound(res, "oauth signing key")
}
