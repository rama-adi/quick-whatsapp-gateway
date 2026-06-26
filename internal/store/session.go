package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// SessionRepo is the repository for wa_sessions (attached WhatsApp numbers, §7).
type SessionRepo struct {
	db dbExecQuerier
}

// NewSessionRepo constructs a SessionRepo.
func NewSessionRepo(db dbExecQuerier) *SessionRepo { return &SessionRepo{db: db} }

const sessionCols = `id, organization_id, created_by_user_id, gateway_id, label, status,
	wa_jid, wa_lid, phone_number, is_admin_session, auto_read, presence_typing,
	rate_per_min, rate_per_hour, last_connected_at, created_at, updated_at`

func scanSession(s rowScanner) (domain.WASession, error) {
	var v domain.WASession
	err := s.Scan(
		&v.ID, &v.OrganizationID, &v.CreatedByUserID, &v.GatewayID, &v.Label, &v.Status,
		&v.WAJID, &v.WALID, &v.PhoneNumber, &v.IsAdminSession, &v.AutoRead, &v.PresenceTyping,
		&v.RatePerMin, &v.RatePerHour, &v.LastConnectedAt, &v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		return domain.WASession{}, err
	}
	return v, nil
}

// Create inserts a new session row.
func (r *SessionRepo) Create(ctx context.Context, s domain.WASession) error {
	const q = `INSERT INTO wa_sessions
(id, organization_id, created_by_user_id, gateway_id, label, status, wa_jid, wa_lid,
 phone_number, is_admin_session, auto_read, presence_typing, rate_per_min, rate_per_hour,
 last_connected_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, q,
		s.ID, s.OrganizationID, s.CreatedByUserID, s.GatewayID, s.Label, s.Status, s.WAJID, s.WALID,
		s.PhoneNumber, s.IsAdminSession, s.AutoRead, s.PresenceTyping, s.RatePerMin, s.RatePerHour,
		s.LastConnectedAt, s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// Get fetches a session by app id. Maps no-rows to not_found.
func (r *SessionRepo) Get(ctx context.Context, id string) (domain.WASession, error) {
	q := "SELECT " + sessionCols + " FROM wa_sessions WHERE id = ?"
	v, err := scanSession(r.db.QueryRowContext(ctx, q, id))
	if err != nil {
		return domain.WASession{}, notFound(err, "session")
	}
	return v, nil
}

// GetByJID fetches a session by its (unique) phone JID. Maps no-rows to not_found.
func (r *SessionRepo) GetByJID(ctx context.Context, jid string) (domain.WASession, error) {
	q := "SELECT " + sessionCols + " FROM wa_sessions WHERE wa_jid = ?"
	v, err := scanSession(r.db.QueryRowContext(ctx, q, jid))
	if err != nil {
		return domain.WASession{}, notFound(err, "session")
	}
	return v, nil
}

// ListByOrg returns all sessions for an organization ordered by created_at desc.
// Session counts per org are small, so this is unpaginated.
func (r *SessionRepo) ListByOrg(ctx context.Context, organizationID string) ([]domain.WASession, error) {
	q := "SELECT " + sessionCols + " FROM wa_sessions WHERE organization_id = ? ORDER BY created_at DESC"
	rows, err := r.db.QueryContext(ctx, q, organizationID)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()
	var out []domain.WASession
	for rows.Next() {
		v, err := scanSession(rows)
		if err != nil {
			return nil, scanErr("wa_sessions", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListAll returns every session (platform super_admin cross-org oversight, §11).
// Kept unpaginated for single-instance scale.
func (r *SessionRepo) ListAll(ctx context.Context) ([]domain.WASession, error) {
	q := "SELECT " + sessionCols + " FROM wa_sessions ORDER BY created_at DESC"
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list all sessions: %w", err)
	}
	defer rows.Close()
	var out []domain.WASession
	for rows.Next() {
		v, err := scanSession(rows)
		if err != nil {
			return nil, scanErr("wa_sessions", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListByGateway returns every session pinned to the given gateway_id, ordered by
// created_at. The boot orphan-guard (Stage 4) uses this to reconcile the sessions
// this gateway is responsible for against its local keystore.
func (r *SessionRepo) ListByGateway(ctx context.Context, gatewayID string) ([]domain.WASession, error) {
	q := "SELECT " + sessionCols + " FROM wa_sessions WHERE gateway_id = ? ORDER BY created_at ASC"
	rows, err := r.db.QueryContext(ctx, q, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions by gateway: %w", err)
	}
	defer rows.Close()
	var out []domain.WASession
	for rows.Next() {
		v, err := scanSession(rows)
		if err != nil {
			return nil, scanErr("wa_sessions", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Update writes the mutable fields of a session (settings + WA identity), keying
// on id and refreshing updated_at from the struct.
func (r *SessionRepo) Update(ctx context.Context, s domain.WASession) error {
	const q = `UPDATE wa_sessions SET
		label=?, status=?, wa_jid=?, wa_lid=?, phone_number=?, auto_read=?,
		presence_typing=?, rate_per_min=?, rate_per_hour=?, last_connected_at=?,
		updated_at=?
	WHERE id=?`
	res, err := r.db.ExecContext(ctx, q,
		s.Label, s.Status, s.WAJID, s.WALID, s.PhoneNumber, s.AutoRead,
		s.PresenceTyping, s.RatePerMin, s.RatePerHour, s.LastConnectedAt,
		s.UpdatedAt, s.ID,
	)
	if err != nil {
		return fmt.Errorf("store: update session: %w", err)
	}
	return affectedOrNotFound(res, "session")
}

// UpdateStatus is the hot path for the session lifecycle (§3): flip status and
// touch updated_at without rewriting the whole row.
func (r *SessionRepo) UpdateStatus(ctx context.Context, id string, status domain.SessionStatus, updatedAt int64) error {
	const q = "UPDATE wa_sessions SET status=?, updated_at=? WHERE id=?"
	res, err := r.db.ExecContext(ctx, q, status, updatedAt, id)
	if err != nil {
		return fmt.Errorf("store: update session status: %w", err)
	}
	return affectedOrNotFound(res, "session")
}

// Delete removes a session by id.
func (r *SessionRepo) Delete(ctx context.Context, id string) error {
	const q = "DELETE FROM wa_sessions WHERE id=?"
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return affectedOrNotFound(res, "session")
}
