package store

import (
	"context"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

// SessionRepo is the repository for wa_sessions (attached WhatsApp numbers, §7).
type SessionRepo struct {
	q *storedb.Queries
}

// NewSessionRepo constructs a SessionRepo.
func NewSessionRepo(db storedb.DBTX) *SessionRepo { return &SessionRepo{q: storedb.New(db)} }

// sessionRow is the shape every session read returns: the intent columns of
// wa_sessions, the observed runtime, and whether the session's gateway is
// connected. sqlc emits one identical struct per query; they convert to it.
type sessionRow = storedb.GetSessionRow

// sessionFromRow resolves the effective status at read time: a live observed
// status (starting, scan_qr_code, working, reconnecting) on a gateway that is
// not connected reads as unavailable, since nothing can confirm it.
func sessionFromRow(row sessionRow) domain.WASession {
	status := domain.SessionStatus(row.RuntimeStatus)
	if status.Live() && !row.GatewayConnected {
		status = domain.SessionUnavailable
	}
	var failure *domain.SessionFailureReason
	if status == domain.SessionFailed && row.RuntimeFailureReason.Valid {
		reason := domain.SessionFailureReason(row.RuntimeFailureReason.WaSessionRuntimeFailureReason)
		failure = &reason
	}
	return domain.WASession{
		ID:               row.ID,
		OrganizationID:   row.OrganizationID,
		CreatedByUserID:  stringPtrFromNull(row.CreatedByUserID),
		GatewayID:        row.GatewayID,
		Label:            stringPtrFromNull(row.Label),
		Status:           status,
		FailureReason:    failure,
		StatusChangedAt:  row.RuntimeStatusChangedAt,
		DesiredState:     domain.DesiredState(row.DesiredState),
		StartGeneration:  row.StartGeneration,
		WAJID:            stringPtrFromNull(row.WaJid),
		WALID:            stringPtrFromNull(row.WaLid),
		PhoneNumber:      stringPtrFromNull(row.PhoneNumber),
		IsAdminSession:   row.IsAdminSession,
		AutoRead:         row.AutoRead,
		PresenceTyping:   row.PresenceTyping,
		RatePerMin:       int(row.RatePerMin),
		RatePerHour:      int(row.RatePerHour),
		LastConnectedAt:  int64PtrFromNull(row.LastConnectedAt),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
		HistorySyncDays:  int(row.HistorySyncDays),
		DataAccountJID:   stringPtrFromNull(row.DataAccountJid),
		AccountChangedAt: int64PtrFromNull(row.AccountChangedAt),
	}
}

// Create inserts a new session row and its runtime row (stopped, no gateway
// observation yet). Run it inside a transaction (GatewayAssignmentRepo.CreateSession).
func (r *SessionRepo) Create(ctx context.Context, s domain.WASession) error {
	desired := s.DesiredState
	if desired == "" {
		desired = domain.DesiredStop
	}
	err := r.q.CreateSession(ctx, storedb.CreateSessionParams{
		ID:              s.ID,
		OrganizationID:  s.OrganizationID,
		CreatedByUserID: nullString(s.CreatedByUserID),
		GatewayID:       s.GatewayID,
		Label:           nullString(s.Label),
		DesiredState:    storedb.WaSessionsDesiredState(desired),
		StartGeneration: s.StartGeneration,
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
		HistorySyncDays: uint16(min(max(s.HistorySyncDays, 0), 65535)), //nolint:gosec // clamped to the column range
	})
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	if err := r.q.CreateSessionRuntime(ctx, storedb.CreateSessionRuntimeParams{
		SessionID: s.ID, StatusChangedAt: s.CreatedAt,
	}); err != nil {
		return fmt.Errorf("store: create session runtime: %w", err)
	}
	return nil
}

// Get fetches a session by app id. Maps no-rows to not_found.
func (r *SessionRepo) Get(ctx context.Context, id string) (domain.WASession, error) {
	row, err := r.q.GetSession(ctx, storedb.GetSessionParams{ID: id})
	if err != nil {
		return domain.WASession{}, notFound(err, "session")
	}
	return sessionFromRow(sessionRow(row)), nil
}

// GetByJID fetches a session by its (unique) phone JID. Maps no-rows to not_found.
func (r *SessionRepo) GetByJID(ctx context.Context, jid string) (domain.WASession, error) {
	row, err := r.q.GetSessionByJID(ctx, storedb.GetSessionByJIDParams{WaJid: nullString(&jid)})
	if err != nil {
		return domain.WASession{}, notFound(err, "session")
	}
	return sessionFromRow(sessionRow(row)), nil
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
		out = append(out, sessionFromRow(sessionRow(row)))
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
		out = append(out, sessionFromRow(sessionRow(row)))
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
		out = append(out, sessionFromRow(sessionRow(row)))
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

// AttachPairingInput carries the PairSuccess identity for
// SessionStateRepo.AttachPairing.
type AttachPairingInput struct {
	SessionID   string
	WaJID       string
	WaLID       string
	PhoneNumber string
	UpdatedAt   int64
}

// Delete removes a session by id.
func (r *SessionRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeleteSession(ctx, storedb.DeleteSessionParams{ID: id})
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return rowsAffectedOrNotFound(n, "session")
}
