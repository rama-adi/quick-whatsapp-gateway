package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// OAuthClientRepo persists registered relying-party configuration with explicit
// organization scoping on every tenant-facing lookup and mutation.
type OAuthClientRepo struct{ db dbExecQuerier }

// OAuthGrantRepo stores durable user consent. Active lookups exclude revoked
// grants, while audit/management lookups may intentionally include them.
type OAuthGrantRepo struct{ db dbExecQuerier }

// OAuthRefreshTokenRepo owns refresh-token families and their rotation state.
// Raw tokens never enter this layer; callers provide only cryptographic hashes.
type OAuthRefreshTokenRepo struct{ db dbExecQuerier }

// OAuthSigningKeyRepo owns issuer key lifecycle state. Private key material is
// already encrypted before persistence and public-list queries omit it.
type OAuthSigningKeyRepo struct{ db dbExecQuerier }

var errOAuthRefreshReuse = errors.New("oauth refresh token reused")

func NewOAuthClientRepo(db dbExecQuerier) *OAuthClientRepo { return &OAuthClientRepo{db: db} }
func NewOAuthGrantRepo(db dbExecQuerier) *OAuthGrantRepo   { return &OAuthGrantRepo{db: db} }
func NewOAuthRefreshTokenRepo(db dbExecQuerier) *OAuthRefreshTokenRepo {
	return &OAuthRefreshTokenRepo{db: db}
}
func NewOAuthSigningKeyRepo(db dbExecQuerier) *OAuthSigningKeyRepo {
	return &OAuthSigningKeyRepo{db: db}
}

const oauthClientCols = `id, client_id, organization_id, created_by_user_id, session_id, name, bot_name, logo_url, client_type, login_command, secret_hash, secret_last4, redirect_uris, modes, group_jid, allowed_scopes, token_ttl_seconds, refresh_ttl_seconds, status, created_at, updated_at, deleted_at`

func scanOAuthClient(s rowScanner) (domain.OAuthClient, error) {
	var c domain.OAuthClient
	var createdBy, botName, logo, secretLast4, group sql.NullString
	var deleted sql.NullInt64
	if err := s.Scan(&c.ID, &c.ClientID, &c.OrganizationID, &createdBy, &c.SessionID, &c.Name, &botName, &logo, &c.ClientType, &c.LoginCommand, &c.SecretHash, &secretLast4, &c.RedirectURIs, &c.Modes, &group, &c.AllowedScopes, &c.TokenTTLSeconds, &c.RefreshTTLSeconds, &c.Status, &c.CreatedAt, &c.UpdatedAt, &deleted); err != nil {
		return domain.OAuthClient{}, scanErr("oauth_clients", err)
	}
	c.CreatedByUserID, c.BotName, c.LogoURL, c.SecretLast4, c.GroupJID = stringPtrFromNull(createdBy), stringPtrFromNull(botName), stringPtrFromNull(logo), stringPtrFromNull(secretLast4), stringPtrFromNull(group)
	c.DeletedAt = int64PtrFromNull(deleted)
	return c, nil
}

func (r *OAuthClientRepo) Create(ctx context.Context, c domain.OAuthClient) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO oauth_clients (`+oauthClientCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ClientID, c.OrganizationID, nullString(c.CreatedByUserID), c.SessionID, c.Name, nullString(c.BotName), nullString(c.LogoURL), c.ClientType, c.LoginCommand, c.SecretHash, nullString(c.SecretLast4), c.RedirectURIs, c.Modes, nullString(c.GroupJID), c.AllowedScopes, c.TokenTTLSeconds, c.RefreshTTLSeconds, c.Status, c.CreatedAt, c.UpdatedAt, nullInt64(c.DeletedAt))
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

func (r *OAuthClientRepo) ListActiveBySession(ctx context.Context, sessionID string) ([]domain.OAuthClient, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+oauthClientCols+` FROM oauth_clients WHERE session_id = ? AND status = 'active' AND deleted_at IS NULL ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list active oauth clients by session: %w", err)
	}
	defer rows.Close()
	var items []domain.OAuthClient
	for rows.Next() {
		c, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list active oauth clients by session: %w", err)
	}
	return items, nil
}

func (r *OAuthClientRepo) ListBySession(ctx context.Context, sessionID string) ([]domain.OAuthClient, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+oauthClientCols+` FROM oauth_clients WHERE session_id = ? AND deleted_at IS NULL ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list oauth clients by session: %w", err)
	}
	defer rows.Close()
	var items []domain.OAuthClient
	for rows.Next() {
		c, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list oauth clients by session: %w", err)
	}
	return items, nil
}

func (r *OAuthClientRepo) DisableActiveBySession(ctx context.Context, sessionID string, updatedAt int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE oauth_clients SET status = 'disabled', updated_at = ? WHERE session_id = ? AND status = 'active' AND deleted_at IS NULL`, updatedAt, sessionID)
	if err != nil {
		return fmt.Errorf("store: disable oauth clients by session: %w", err)
	}
	return nil
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
	res, err := r.db.ExecContext(ctx, `UPDATE oauth_clients SET session_id = ?, name = ?, bot_name = ?, logo_url = ?, client_type = ?, login_command = ?, secret_hash = ?, secret_last4 = ?, redirect_uris = ?, modes = ?, group_jid = ?, allowed_scopes = ?, token_ttl_seconds = ?, refresh_ttl_seconds = ?, status = ?, updated_at = ? WHERE organization_id = ? AND id = ? AND deleted_at IS NULL`,
		c.SessionID, c.Name, nullString(c.BotName), nullString(c.LogoURL), c.ClientType, c.LoginCommand, c.SecretHash, nullString(c.SecretLast4), c.RedirectURIs, c.Modes, nullString(c.GroupJID), c.AllowedScopes, c.TokenTTLSeconds, c.RefreshTTLSeconds, c.Status, c.UpdatedAt, c.OrganizationID, c.ID)
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

func (r *OAuthGrantRepo) UpsertAndGet(ctx context.Context, g domain.OAuthGrant) (domain.OAuthGrant, error) {
	if err := r.Upsert(ctx, g); err != nil {
		return domain.OAuthGrant{}, err
	}
	return r.GetActiveByClientIdentity(ctx, g.OrganizationID, g.ClientID, g.WAIdentityID)
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

func (r *OAuthGrantRepo) ListActiveIDsByClient(ctx context.Context, orgID, clientID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM oauth_grants WHERE organization_id = ? AND client_id = ? AND revoked_at IS NULL ORDER BY id ASC`, orgID, clientID)
	if err != nil {
		return nil, fmt.Errorf("store: list active oauth grant ids by client: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: list active oauth grant ids by client: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list active oauth grant ids by client: %w", err)
	}
	return ids, nil
}

func (r *OAuthGrantRepo) GetActiveByClientIdentity(ctx context.Context, orgID, clientID string, identityID uint64) (domain.OAuthGrant, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants WHERE organization_id = ? AND client_id = ? AND wa_identity_id = ? AND revoked_at IS NULL`, orgID, clientID, identityID)
	g, err := scanOAuthGrant(row)
	if err != nil {
		return domain.OAuthGrant{}, notFound(err, "oauth grant")
	}
	return g, nil
}

func (r *OAuthGrantRepo) GetActiveByID(ctx context.Context, id string) (domain.OAuthGrant, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants WHERE id = ? AND revoked_at IS NULL`, id)
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

func (r *OAuthRefreshTokenRepo) RevokeTokenHash(ctx context.Context, tokenHash []byte, revokedAt int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`, revokedAt, tokenHash)
	if err != nil {
		return fmt.Errorf("store: revoke oauth refresh token: %w", err)
	}
	return nil
}

func (r *OAuthRefreshTokenRepo) CountActiveFamiliesByGrant(ctx context.Context, orgID, grantID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT family_id) FROM oauth_refresh_tokens WHERE organization_id = ? AND grant_id = ? AND revoked_at IS NULL AND expires_at > ?`, orgID, grantID, domain.NowMs()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count oauth refresh families: %w", err)
	}
	return n, nil
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

// RotateRefreshToken locks the presented token and its active grant, consumes
// the token exactly once, and inserts its successor in the same transaction.
// Reuse commits family revocation before returning errOAuthRefreshReuse; all
// other failures roll back so a caller never observes a half-rotation.
func (r *OAuthRefreshTokenRepo) RotateRefreshToken(ctx context.Context, rot domain.OAuthRefreshRotation) (domain.OAuthRefreshToken, domain.OAuthGrant, error) {
	db, ok := r.db.(*sql.DB)
	if !ok {
		return r.rotateRefreshToken(ctx, r.db, rot)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, fmt.Errorf("store: begin oauth refresh rotation: %w", err)
	}
	defer tx.Rollback()
	rt, g, err := r.rotateRefreshToken(ctx, tx, rot)
	if err != nil {
		if errors.Is(err, errOAuthRefreshReuse) {
			if commitErr := tx.Commit(); commitErr != nil {
				return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, fmt.Errorf("store: commit oauth refresh reuse revocation: %w", commitErr)
			}
			return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, err
		}
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, fmt.Errorf("store: commit oauth refresh rotation: %w", err)
	}
	return rt, g, nil
}

// rotateRefreshToken contains the lock-ordered rotation statements and assumes
// q belongs to a transaction when the concrete repository is backed by *sql.DB.
// Token is locked before grant consistently to avoid deadlocks between workers.
func (r *OAuthRefreshTokenRepo) rotateRefreshToken(ctx context.Context, q dbExecQuerier, rot domain.OAuthRefreshRotation) (domain.OAuthRefreshToken, domain.OAuthGrant, error) {
	row := q.QueryRowContext(ctx, `SELECT `+oauthRefreshTokenCols+` FROM oauth_refresh_tokens WHERE token_hash = ? FOR UPDATE`, rot.TokenHash)
	rt, err := scanOAuthRefreshToken(row)
	if err != nil {
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, notFound(err, "oauth refresh token")
	}
	if rt.ConsumedAt != nil {
		_, _ = q.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL`, rot.Now, rt.FamilyID)
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, errOAuthRefreshReuse
	}
	if rt.ExpiresAt <= rot.Now || rt.RevokedAt != nil {
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, domain.ErrNotFound("oauth refresh token inactive")
	}
	row = q.QueryRowContext(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants WHERE id = ? AND revoked_at IS NULL FOR UPDATE`, rt.GrantID)
	g, err := scanOAuthGrant(row)
	if err != nil {
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, notFound(err, "oauth grant")
	}
	if g.ClientID != rot.ClientID {
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, domain.ErrNotFound("oauth grant client mismatch")
	}
	scopes := []string{}
	_ = json.Unmarshal(rt.Scopes, &scopes)
	if len(rot.RequestedScopes) > 0 {
		if !scopeSubsetStore(rot.RequestedScopes, scopes) {
			return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, domain.ErrOAuthScopeWidening
		}
		scopes = rot.RequestedScopes
	}
	scopesJSON, _ := json.Marshal(scopes)
	successor := rot.Successor
	successor.GrantID = rt.GrantID
	successor.OrganizationID = rt.OrganizationID
	successor.FamilyID = rt.FamilyID
	successor.ParentID = &rt.ID
	successor.Scopes = scopesJSON
	successor.IssuedAt = rot.Now
	successor.ExpiresAt = rt.ExpiresAt
	res, err := q.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, rot.Now, rt.ID)
	if err != nil {
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, fmt.Errorf("store: consume oauth refresh token: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		_, _ = q.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL`, rot.Now, rt.FamilyID)
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, errOAuthRefreshReuse
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens (`+oauthRefreshTokenCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		successor.ID, successor.GrantID, successor.OrganizationID, successor.TokenHash, successor.FamilyID, nullString(successor.ParentID), successor.Scopes, successor.IssuedAt, successor.ExpiresAt, nullInt64(successor.ConsumedAt), nullInt64(successor.RevokedAt)); err != nil {
		return domain.OAuthRefreshToken{}, domain.OAuthGrant{}, fmt.Errorf("store: create oauth refresh successor: %w", err)
	}
	return rt, g, nil
}

func scopeSubsetStore(next, base []string) bool {
	have := map[string]bool{}
	for _, s := range base {
		have[s] = true
	}
	for _, s := range next {
		if !have[s] {
			return false
		}
	}
	return true
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

// PromoteNext retires the current active key and promotes the named next key in
// one transaction. If the named key is absent or any statement fails, rollback
// preserves the previous active issuer key.
func (r *OAuthSigningKeyRepo) PromoteNext(ctx context.Context, kid string, retiredAt int64) error {
	db, ok := r.db.(*sql.DB)
	if !ok {
		return r.promoteNext(ctx, r.db, kid, retiredAt)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin oauth signing key promote: %w", err)
	}
	defer tx.Rollback()
	if err := r.promoteNext(ctx, tx, kid, retiredAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit oauth signing key promote: %w", err)
	}
	return nil
}

// promoteNext performs the ordered state transition within its caller-owned
// transaction and requires exactly one next key to be promoted.
func (r *OAuthSigningKeyRepo) promoteNext(ctx context.Context, q dbExecQuerier, kid string, retiredAt int64) error {
	res, err := q.ExecContext(ctx, `UPDATE oauth_signing_keys SET status = 'retired', retired_at = ? WHERE status = 'active'`, retiredAt)
	if err != nil {
		return fmt.Errorf("store: retire active oauth signing key: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 1 {
		return errors.New("store: multiple active oauth signing keys")
	}
	res, err = q.ExecContext(ctx, `UPDATE oauth_signing_keys SET status = 'active' WHERE kid = ? AND status = 'next'`, kid)
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
