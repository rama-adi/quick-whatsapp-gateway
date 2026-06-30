package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// SessionRepo is the repository for wa_sessions (attached WhatsApp numbers, §7).
type SessionRepo struct {
	q *storedb.Queries
}

// NewSessionRepo constructs a SessionRepo.
func NewSessionRepo(db storedb.DBTX) *SessionRepo { return &SessionRepo{q: storedb.New(db)} }

func sessionFromRow(row storedb.WaSession) domain.WASession {
	return domain.WASession{
		ID:              row.ID,
		OrganizationID:  row.OrganizationID,
		CreatedByUserID: stringPtrFromNull(row.CreatedByUserID),
		GatewayID:       row.GatewayID,
		Label:           stringPtrFromNull(row.Label),
		Status:          domain.SessionStatus(row.Status),
		WAJID:           stringPtrFromNull(row.WaJid),
		WALID:           stringPtrFromNull(row.WaLid),
		PhoneNumber:     stringPtrFromNull(row.PhoneNumber),
		IsAdminSession:  row.IsAdminSession,
		AutoRead:        row.AutoRead,
		PresenceTyping:  row.PresenceTyping,
		RatePerMin:      int(row.RatePerMin),
		RatePerHour:     int(row.RatePerHour),
		LastConnectedAt: int64PtrFromNull(row.LastConnectedAt),
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

// Create inserts a new session row.
func (r *SessionRepo) Create(ctx context.Context, s domain.WASession) error {
	err := r.q.CreateSession(ctx, storedb.CreateSessionParams{
		ID:              s.ID,
		OrganizationID:  s.OrganizationID,
		CreatedByUserID: nullString(s.CreatedByUserID),
		GatewayID:       s.GatewayID,
		Label:           nullString(s.Label),
		Status:          storedb.WaSessionsStatus(s.Status),
		WaJid:           nullString(s.WAJID),
		WaLid:           nullString(s.WALID),
		PhoneNumber:     nullString(s.PhoneNumber),
		IsAdminSession:  s.IsAdminSession,
		AutoRead:        s.AutoRead,
		PresenceTyping:  s.PresenceTyping,
		RatePerMin:      int32(s.RatePerMin),
		RatePerHour:     int32(s.RatePerHour),
		LastConnectedAt: nullInt64(s.LastConnectedAt),
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// Get fetches a session by app id. Maps no-rows to not_found.
func (r *SessionRepo) Get(ctx context.Context, id string) (domain.WASession, error) {
	row, err := r.q.GetSession(ctx, storedb.GetSessionParams{ID: id})
	if err != nil {
		return domain.WASession{}, notFound(err, "session")
	}
	return sessionFromRow(row), nil
}

// GetByJID fetches a session by its (unique) phone JID. Maps no-rows to not_found.
func (r *SessionRepo) GetByJID(ctx context.Context, jid string) (domain.WASession, error) {
	row, err := r.q.GetSessionByJID(ctx, storedb.GetSessionByJIDParams{WaJid: nullString(&jid)})
	if err != nil {
		return domain.WASession{}, notFound(err, "session")
	}
	return sessionFromRow(row), nil
}

// ListByOrg returns all sessions for an organization ordered by created_at desc.
// Session counts per org are small, so this is unpaginated.
func (r *SessionRepo) ListByOrg(ctx context.Context, organizationID string) ([]domain.WASession, error) {
	rows, err := r.q.ListSessionsByOrg(ctx, storedb.ListSessionsByOrgParams{OrganizationID: organizationID})
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	out := make([]domain.WASession, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionFromRow(row))
	}
	return out, nil
}

// ListAll returns every session (platform super_admin cross-org oversight, §11).
// Kept unpaginated for single-instance scale.
func (r *SessionRepo) ListAll(ctx context.Context) ([]domain.WASession, error) {
	rows, err := r.q.ListAllSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: list all sessions: %w", err)
	}
	out := make([]domain.WASession, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionFromRow(row))
	}
	return out, nil
}

// ListByGateway returns every session pinned to the given gateway_id, ordered by
// created_at. The boot orphan-guard (Stage 4) uses this to reconcile the sessions
// this gateway is responsible for against its local keystore.
func (r *SessionRepo) ListByGateway(ctx context.Context, gatewayID string) ([]domain.WASession, error) {
	rows, err := r.q.ListSessionsByGateway(ctx, storedb.ListSessionsByGatewayParams{GatewayID: gatewayID})
	if err != nil {
		return nil, fmt.Errorf("store: list sessions by gateway: %w", err)
	}
	out := make([]domain.WASession, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionFromRow(row))
	}
	return out, nil
}

// CountByGateway returns how many sessions are pinned to the given gateway_id. The
// gateway heartbeat reports this into the registry (gateways.session_count) so the
// router can place new sessions on the least-loaded gateway (D8).
func (r *SessionRepo) CountByGateway(ctx context.Context, gatewayID string) (int, error) {
	n, err := r.q.CountSessionsByGateway(ctx, storedb.CountSessionsByGatewayParams{GatewayID: gatewayID})
	if err != nil {
		return 0, fmt.Errorf("store: count sessions by gateway: %w", err)
	}
	return int(n), nil
}

// Update writes the mutable fields of a session (settings + WA identity), keying
// on id and refreshing updated_at from the struct.
func (r *SessionRepo) Update(ctx context.Context, s domain.WASession) error {
	n, err := r.q.UpdateSession(ctx, storedb.UpdateSessionParams{
		Label:           nullString(s.Label),
		Status:          storedb.WaSessionsStatus(s.Status),
		WaJid:           nullString(s.WAJID),
		WaLid:           nullString(s.WALID),
		PhoneNumber:     nullString(s.PhoneNumber),
		AutoRead:        s.AutoRead,
		PresenceTyping:  s.PresenceTyping,
		RatePerMin:      int32(s.RatePerMin),
		RatePerHour:     int32(s.RatePerHour),
		LastConnectedAt: nullInt64(s.LastConnectedAt),
		UpdatedAt:       s.UpdatedAt,
		ID:              s.ID,
	})
	if err != nil {
		return fmt.Errorf("store: update session: %w", err)
	}
	return rowsAffectedOrNotFound(n, "session")
}

// UpdateStatus is the hot path for the session lifecycle (§3): flip status and
// touch updated_at without rewriting the whole row.
func (r *SessionRepo) UpdateStatus(ctx context.Context, id string, status domain.SessionStatus, updatedAt int64) error {
	n, err := r.q.UpdateSessionStatus(ctx, storedb.UpdateSessionStatusParams{
		Status:    storedb.WaSessionsStatus(status),
		UpdatedAt: updatedAt,
		ID:        id,
	})
	if err != nil {
		return fmt.Errorf("store: update session status: %w", err)
	}
	return rowsAffectedOrNotFound(n, "session")
}

// Delete removes a session by id.
func (r *SessionRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeleteSession(ctx, storedb.DeleteSessionParams{ID: id})
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return rowsAffectedOrNotFound(n, "session")
}
