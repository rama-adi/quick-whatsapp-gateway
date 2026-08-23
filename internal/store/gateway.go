package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// GatewayRepo is the repository for the gateways registry (§7) — the routing table
// the central router reads to place sessions and proxy requests. Each row is a
// gateway's self-reported identity, reachability (base_url), lifecycle status, and
// load (session_count/capacity). The owning gateway writes its own row
// (Register/Heartbeat/SetStatus); the router reads the table (Get/ListActive/
// PickForPlacement).
type GatewayRepo struct {
	q *storedb.Queries
}

const connectionEpochAllocationAttempts = 8

// NewGatewayRepo constructs a GatewayRepo.
func NewGatewayRepo(db storedb.DBTX) *GatewayRepo { return &GatewayRepo{q: storedb.New(db)} }

func gatewayFromRow(row storedb.GetGatewayRow) domain.Gateway {
	return domain.Gateway{
		ID:                   row.ID,
		Label:                stringPtrFromNull(row.Label),
		Notes:                stringPtrFromNull(row.Notes),
		Status:               domain.GatewayStatus(row.Status),
		SessionCount:         int(row.SessionCount),
		Capacity:             intPtrFromNull32(row.Capacity),
		BaseURL:              stringPtrFromNull(row.BaseUrl),
		GRPCEndpoint:         stringPtrFromNull(row.GrpcEndpoint),
		SoftwareVersion:      stringPtrFromNull(row.SoftwareVersion),
		Capabilities:         append(json.RawMessage(nil), row.Capabilities...),
		ConnectionEpoch:      row.ConnectionEpoch,
		ConnectionMode:       string(row.ConnectionMode),
		DesiredLifecycle:     string(row.DesiredLifecycle),
		DesiredRevision:      row.DesiredRevision,
		AppliedRevision:      row.AppliedRevision,
		ReconciliationStatus: string(row.ReconciliationStatus),
		KeystorePresent:      boolPtrFromNull(row.KeystorePresent),
		KeystoreBytes:        int64PtrFromNull(row.KeystoreBytes),
		KeystoreIntegrity:    gatewayKeystoreIntegrityPtr(row.KeystoreIntegrity),
		KeystoreCheckedAt:    int64PtrFromNull(row.KeystoreCheckedAt),
		JournalState:         journalStatePtr(row.JournalState.GatewaysJournalState, row.JournalState.Valid),
		JournalEntries:       uint64PtrFromNull(row.JournalEntries),
		JournalBytes:         uint64PtrFromNull(row.JournalBytes),
		EnrolledAt:           int64PtrFromNull(row.EnrolledAt),
		ConnectedAt:          int64PtrFromNull(row.ConnectedAt),
		LastSeenAt:           int64PtrFromNull(row.LastSeenAt),
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
	}
}

func gatewayFromListActiveRow(row storedb.ListActiveGatewaysRow) domain.Gateway {
	return domain.Gateway{
		ID:               row.ID,
		Label:            stringPtrFromNull(row.Label),
		Notes:            stringPtrFromNull(row.Notes),
		Status:           domain.GatewayStatus(row.Status),
		SessionCount:     int(row.SessionCount),
		Capacity:         intPtrFromNull32(row.Capacity),
		BaseURL:          stringPtrFromNull(row.BaseUrl),
		GRPCEndpoint:     stringPtrFromNull(row.GrpcEndpoint),
		SoftwareVersion:  stringPtrFromNull(row.SoftwareVersion),
		Capabilities:     append(json.RawMessage(nil), row.Capabilities...),
		ConnectionEpoch:  row.ConnectionEpoch,
		ConnectionMode:   string(row.ConnectionMode),
		DesiredLifecycle: string(row.DesiredLifecycle),
		DesiredRevision:  row.DesiredRevision,
		AppliedRevision:  row.AppliedRevision,
		EnrolledAt:       int64PtrFromNull(row.EnrolledAt),
		ConnectedAt:      int64PtrFromNull(row.ConnectedAt),
		LastSeenAt:       int64PtrFromNull(row.LastSeenAt),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func gatewayFromPlacementRow(row storedb.PickGatewayForPlacementRow) domain.Gateway {
	return domain.Gateway{
		ID:               row.ID,
		Label:            stringPtrFromNull(row.Label),
		Notes:            stringPtrFromNull(row.Notes),
		Status:           domain.GatewayStatus(row.Status),
		SessionCount:     int(row.SessionCount),
		Capacity:         intPtrFromNull32(row.Capacity),
		BaseURL:          stringPtrFromNull(row.BaseUrl),
		GRPCEndpoint:     stringPtrFromNull(row.GrpcEndpoint),
		SoftwareVersion:  stringPtrFromNull(row.SoftwareVersion),
		Capabilities:     append(json.RawMessage(nil), row.Capabilities...),
		ConnectionEpoch:  row.ConnectionEpoch,
		ConnectionMode:   string(row.ConnectionMode),
		DesiredLifecycle: string(row.DesiredLifecycle),
		DesiredRevision:  row.DesiredRevision,
		AppliedRevision:  row.AppliedRevision,
		EnrolledAt:       int64PtrFromNull(row.EnrolledAt),
		ConnectedAt:      int64PtrFromNull(row.ConnectedAt),
		LastSeenAt:       int64PtrFromNull(row.LastSeenAt),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func gatewayFromListRow(row storedb.ListGatewaysRow) domain.Gateway {
	return gatewayFromRow(storedb.GetGatewayRow(row))
}

// Upsert inserts or updates this gateway's registry row by id (= GATEWAY_ID).
// created_at is preserved on update; the mutable fields and updated_at refresh.
// This is the boot self-registration path (status transitions to joining→active);
// the heartbeat (Heartbeat) maintains last_seen_at + session_count thereafter, and
// is intentionally NOT clobbered here so a heartbeat racing a re-register is safe.
func (r *GatewayRepo) Upsert(ctx context.Context, g domain.Gateway) error {
	err := r.q.UpsertGateway(ctx, storedb.UpsertGatewayParams{
		ID:           g.ID,
		Label:        nullString(g.Label),
		Status:       storedb.GatewaysStatus(g.Status),
		SessionCount: uint32(g.SessionCount),
		Capacity:     nullInt32(g.Capacity),
		BaseUrl:      nullString(g.BaseURL),
		LastSeenAt:   nullInt64(g.LastSeenAt),
		CreatedAt:    g.CreatedAt,
		UpdatedAt:    g.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("store: upsert gateway: %w", err)
	}
	return nil
}

// CreatePending creates an operator-owned gateway awaiting enrollment. Unlike
// bootstrap Upsert, creatorID is required and persisted as creator_kind=user.
func (r *GatewayRepo) CreatePending(
	ctx context.Context,
	g domain.Gateway,
	notes, creatorID *string,
	desiredRevision uint64,
) error {
	if creatorID == nil || *creatorID == "" {
		return fmt.Errorf("store: gateway creator is required")
	}
	return r.q.CreateGateway(ctx, storedb.CreateGatewayParams{
		ID:              g.ID,
		Label:           nullString(g.Label),
		Notes:           nullString(notes),
		Status:          storedb.GatewaysStatusPendingEnrollment,
		CreatedByUserID: nullString(creatorID),
		Capacity:        nullInt32(g.Capacity),
		DesiredRevision: desiredRevision,
		CreatedAt:       g.CreatedAt,
		UpdatedAt:       g.UpdatedAt,
	})
}
func (r *GatewayRepo) markEnrolledJoining(ctx context.Context, id string, at int64) (bool, error) {
	n, e := r.q.EnrollPendingGateway(ctx, storedb.EnrollPendingGatewayParams{
		EnrolledAt: sql.NullInt64{Int64: at, Valid: true},
		UpdatedAt:  at,
		ID:         id,
	})
	return n == 1, e
}

func (r *GatewayRepo) UpdateMetadata(
	ctx context.Context,
	id string,
	label, notes *string,
	capacity *int,
	revision uint64,
	at int64,
) (bool, error) {
	n, err := r.q.UpdateGatewayMetadata(ctx, storedb.UpdateGatewayMetadataParams{
		Label:           nullString(label),
		Notes:           nullString(notes),
		Capacity:        nullInt32(capacity),
		DesiredRevision: revision,
		UpdatedAt:       at,
		ID:              id,
	})
	return n == 1, err
}

func (r *GatewayRepo) Disable(ctx context.Context, id string, at int64) (bool, error) {
	n, err := r.q.DisableGateway(ctx, storedb.DisableGatewayParams{UpdatedAt: at, ID: id})
	return n == 1, err
}

// SoftDelete retains enrollment/certificate history and only hides a safely
// quiesced gateway with no live credential material.
func (r *GatewayRepo) SoftDelete(ctx context.Context, id string, at int64) (bool, error) {
	n, err := r.q.SoftDeleteGateway(ctx, storedb.SoftDeleteGatewayParams{
		DeletedAt: sql.NullInt64{Int64: at, Valid: true},
		UpdatedAt: at,
		ID:        id,
	})
	return n == 1, err
}

func (r *GatewayRepo) UpdateConnectionMetadata(
	ctx context.Context,
	m domain.GatewayControlMetadata,
	at int64,
) (bool, error) {
	if len(m.Capabilities) > 0 && !json.Valid(m.Capabilities) {
		return false, fmt.Errorf("store: invalid gateway capabilities")
	}
	n, err := r.q.UpdateGatewayConnectionMetadata(ctx, storedb.UpdateGatewayConnectionMetadataParams{
		GrpcEndpoint:    nullString(m.GRPCEndpoint),
		SoftwareVersion: nullString(m.SoftwareVersion),
		Capabilities:    json.RawMessage(m.Capabilities),
		AppliedRevision: m.AppliedRevision,
		ConnectedAt:     nullInt64(m.ConnectedAt),
		UpdatedAt:       at,
		ID:              m.GatewayID,
	})
	return n == 1, err
}

// AcceptConnection atomically claims the next stream incarnation and persists
// its initial report for an enrolled, enabled gateway.
func (r *GatewayRepo) AcceptConnection(
	ctx context.Context,
	hello domain.GatewayConnectionHello,
	at int64,
) (domain.GatewayAcceptedConnection, error) {
	invalidHello := hello.GatewayID == "" || hello.SessionCount < 0 || !gatewayReportedStatus(hello.Status)
	invalidCapabilities := len(hello.Capabilities) > 0 && !json.Valid(hello.Capabilities)
	if invalidHello || invalidCapabilities {
		return domain.GatewayAcceptedConnection{}, fmt.Errorf("store: invalid gateway connection hello")
	}
	for range connectionEpochAllocationAttempts {
		current, err := r.q.GetGatewayConnectionEpochForAllocation(ctx, storedb.GetGatewayConnectionEpochForAllocationParams{
			ID: hello.GatewayID,
		})
		if err != nil {
			return domain.GatewayAcceptedConnection{}, notFound(err, "connectable gateway")
		}
		n, err := r.q.AllocateGatewayConnectionEpoch(ctx, storedb.AllocateGatewayConnectionEpochParams{
			ConnectedAt:     sql.NullInt64{Int64: at, Valid: true},
			LastSeenAt:      sql.NullInt64{Int64: at, Valid: true},
			BaseUrl:         nullString(hello.BaseURL),
			GrpcEndpoint:    nullString(hello.GRPCEndpoint),
			SoftwareVersion: nullString(hello.SoftwareVersion),
			Capabilities:    json.RawMessage(hello.Capabilities),
			SessionCount:    uint32(hello.SessionCount),
			ReportedStatus:  storedb.GatewaysStatus(hello.Status),
			UpdatedAt:       at,
			ID:              hello.GatewayID,
			ConnectionEpoch: current,
		})
		if err != nil {
			return domain.GatewayAcceptedConnection{}, fmt.Errorf("store: allocate gateway connection epoch: %w", err)
		}
		if n == 1 {
			epoch := current + 1
			desired, err := r.q.GetAcceptedGatewayDesiredLifecycle(ctx, storedb.GetAcceptedGatewayDesiredLifecycleParams{
				ID: hello.GatewayID, ConnectionEpoch: epoch,
			})
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return domain.GatewayAcceptedConnection{}, domain.ErrConflict(
						"gateway connection was superseded during acceptance",
					)
				}
				return domain.GatewayAcceptedConnection{}, fmt.Errorf("store: read accepted gateway connection: %w", err)
			}
			return domain.GatewayAcceptedConnection{
				ConnectionEpoch:  epoch,
				DesiredLifecycle: string(desired.DesiredLifecycle),
				DesiredRevision:  desired.DesiredRevision,
			}, nil
		}
	}
	return domain.GatewayAcceptedConnection{},
		fmt.Errorf("store: allocate gateway connection epoch: concurrent allocation contention")
}

func (r *GatewayRepo) HeartbeatForEpoch(
	ctx context.Context,
	h domain.GatewayHeartbeat,
	at int64,
) (domain.GatewayAcceptedConnection, bool, error) {
	if h.ConnectionEpoch == 0 || h.SessionCount < 0 || !gatewayReportedStatus(h.Status) {
		return domain.GatewayAcceptedConnection{}, false, fmt.Errorf("store: invalid fenced gateway heartbeat")
	}
	journalState, journalEntries, journalBytes, err := gatewayJournalTelemetryParams(h)
	if err != nil {
		return domain.GatewayAcceptedConnection{}, false, fmt.Errorf("store: invalid fenced gateway heartbeat: %w", err)
	}
	n, err := r.q.GatewayHeartbeatForEpoch(ctx, storedb.GatewayHeartbeatForEpochParams{
		LastSeenAt:      sql.NullInt64{Int64: at, Valid: true},
		SessionCount:    uint32(h.SessionCount),
		ReportedStatus:  storedb.GatewaysStatus(h.Status),
		UpdatedAt:       at,
		ID:              h.GatewayID,
		ConnectionEpoch: h.ConnectionEpoch,
		JournalState:    journalState,
		JournalEntries:  journalEntries,
		JournalBytes:    journalBytes,
	})
	if err != nil {
		return domain.GatewayAcceptedConnection{}, false, fmt.Errorf("store: fenced gateway heartbeat: %w", err)
	}
	applied, err := r.fencedWriteApplied(ctx, n, h.GatewayConnection)
	if err != nil || !applied {
		return domain.GatewayAcceptedConnection{}, applied, err
	}
	desired, err := r.q.GetAcceptedGatewayDesiredLifecycle(ctx, storedb.GetAcceptedGatewayDesiredLifecycleParams{
		ID: h.GatewayID, ConnectionEpoch: h.ConnectionEpoch,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.GatewayAcceptedConnection{}, false, nil
		}
		return domain.GatewayAcceptedConnection{}, false, fmt.Errorf("store: read fenced gateway desired lifecycle: %w", err)
	}
	return domain.GatewayAcceptedConnection{
		ConnectionEpoch:  h.ConnectionEpoch,
		DesiredLifecycle: string(desired.DesiredLifecycle),
		DesiredRevision:  desired.DesiredRevision,
	}, true, nil
}

// gatewayJournalTelemetryParams validates optional journal telemetry: a state
// must be a known enum value, and entry/byte counts require a state so the
// stored columns never describe an unreported journal.
func gatewayJournalTelemetryParams(
	h domain.GatewayHeartbeat,
) (storedb.NullGatewaysJournalState, sql.NullInt64, sql.NullInt64, error) {
	if h.JournalState == nil {
		if h.JournalEntries != nil || h.JournalBytes != nil {
			return storedb.NullGatewaysJournalState{}, sql.NullInt64{}, sql.NullInt64{},
				fmt.Errorf("journal telemetry counts without state")
		}
		return storedb.NullGatewaysJournalState{}, sql.NullInt64{}, sql.NullInt64{}, nil
	}
	var state storedb.GatewaysJournalState
	switch *h.JournalState {
	case "healthy":
		state = storedb.GatewaysJournalStateHealthy
	case "degraded":
		state = storedb.GatewaysJournalStateDegraded
	case "paused":
		state = storedb.GatewaysJournalStatePaused
	case "critical":
		state = storedb.GatewaysJournalStateCritical
	default:
		return storedb.NullGatewaysJournalState{}, sql.NullInt64{}, sql.NullInt64{},
			fmt.Errorf("unknown journal state %q", *h.JournalState)
	}
	if h.JournalEntries == nil || h.JournalBytes == nil || *h.JournalBytes < 0 {
		return storedb.NullGatewaysJournalState{}, sql.NullInt64{}, sql.NullInt64{},
			fmt.Errorf("journal telemetry requires entry and byte counts")
	}
	return storedb.NullGatewaysJournalState{GatewaysJournalState: state, Valid: true},
		sql.NullInt64{Int64: int64(*h.JournalEntries), Valid: true},
		sql.NullInt64{Int64: int64(*h.JournalBytes), Valid: true}, nil
}

func (r *GatewayRepo) SetStatusForEpoch(
	ctx context.Context,
	report domain.GatewayLifecycleReport,
	at int64,
) (bool, error) {
	if report.ConnectionEpoch == 0 || !gatewayReportedStatus(report.Status) {
		return false, fmt.Errorf("store: invalid fenced gateway lifecycle")
	}
	n, err := r.q.SetGatewayStatusForEpoch(ctx, storedb.SetGatewayStatusForEpochParams{
		ReportedStatus:  storedb.GatewaysStatus(report.Status),
		UpdatedAt:       at,
		ID:              report.GatewayID,
		ConnectionEpoch: report.ConnectionEpoch,
	})
	if err != nil {
		return false, fmt.Errorf("store: fenced gateway lifecycle: %w", err)
	}
	return r.fencedWriteApplied(ctx, n, report.GatewayConnection)
}

// DisconnectForEpoch immediately removes the routing liveness marker without
// changing administrative lifecycle state. The epoch fence prevents an old
// stream's deferred cleanup from evicting a replacement connection.
func (r *GatewayRepo) DisconnectForEpoch(
	ctx context.Context,
	connection domain.GatewayConnection,
	at int64,
) (bool, error) {
	if connection.GatewayID == "" || connection.ConnectionEpoch == 0 {
		return false, fmt.Errorf("store: invalid fenced gateway disconnect")
	}
	n, err := r.q.DisconnectGatewayForEpoch(ctx, storedb.DisconnectGatewayForEpochParams{
		UpdatedAt:       at,
		ID:              connection.GatewayID,
		ConnectionEpoch: connection.ConnectionEpoch,
	})
	if err != nil {
		return false, fmt.Errorf("store: fenced gateway disconnect: %w", err)
	}
	return r.fencedWriteApplied(ctx, n, connection)
}

// DesiredStateForEpoch returns the complete authoritative assignment set only
// while the connection epoch and requested desired revision are current.
func (r *GatewayRepo) DesiredStateForEpoch(
	ctx context.Context,
	connection domain.GatewayConnection,
	revision uint64,
	leaseExpiresAt int64,
) ([]domain.GatewayDesiredSession, bool, error) {
	currentDesired, err := r.q.GetAcceptedGatewayDesiredLifecycle(ctx, storedb.GetAcceptedGatewayDesiredLifecycleParams{
		ID:              connection.GatewayID,
		ConnectionEpoch: connection.ConnectionEpoch,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("store: get gateway desired revision: %w", err)
	}
	if currentDesired.DesiredRevision != revision {
		return nil, false, nil
	}
	rows, err := r.q.ListGatewayDesiredStateAssignments(ctx, storedb.ListGatewayDesiredStateAssignmentsParams{
		GatewayID:       connection.GatewayID,
		ConnectionEpoch: connection.ConnectionEpoch,
		DesiredRevision: revision,
	})
	if err != nil {
		return nil, false, fmt.Errorf("store: list gateway desired state: %w", err)
	}
	finalDesired, err := r.q.GetAcceptedGatewayDesiredLifecycle(ctx, storedb.GetAcceptedGatewayDesiredLifecycleParams{
		ID:              connection.GatewayID,
		ConnectionEpoch: connection.ConnectionEpoch,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("store: recheck gateway desired revision: %w", err)
	}
	if finalDesired.DesiredRevision != revision {
		return nil, false, nil
	}
	out := make([]domain.GatewayDesiredSession, 0, len(rows))
	for _, row := range rows {
		desiredRun := row.Status == storedb.WaSessionsStatusWorking ||
			row.Status == storedb.WaSessionsStatusStarting ||
			row.Status == storedb.WaSessionsStatusScanQrCode
		out = append(out, domain.GatewayDesiredSession{
			SessionID:       row.SessionID,
			OrganizationID:  row.OrganizationID,
			DeviceJID:       row.WaJid.String,
			AssignmentEpoch: row.AssignmentEpoch,
			ConfigRevision:  revision,
			AutoRead:        row.AutoRead,
			PresenceTyping:  row.PresenceTyping,
			RatePerMin:      uint32(row.RatePerMin),
			RatePerHour:     uint32(row.RatePerHour),
			DesiredRun:      desiredRun,
			LeaseExpiresAt:  leaseExpiresAt,
		})
	}
	return out, true, nil
}

// AcknowledgeDesiredStateForEpoch persists an applied snapshot only if the
// stream and snapshot revision remain current. The monotonic predicate refuses
// acknowledgement regression on duplicate or delayed frames.
func (r *GatewayRepo) AcknowledgeDesiredStateForEpoch(
	ctx context.Context,
	connection domain.GatewayConnection,
	revision uint64,
	at int64,
) (bool, error) {
	n, err := r.q.AcknowledgeGatewayDesiredStateForEpoch(ctx, storedb.AcknowledgeGatewayDesiredStateForEpochParams{
		AppliedRevision:   revision,
		UpdatedAt:         at,
		ID:                connection.GatewayID,
		ConnectionEpoch:   connection.ConnectionEpoch,
		DesiredRevision:   revision,
		AppliedRevision_2: revision,
	})
	return n == 1, err
}

func (r *GatewayRepo) UpdateConnectionMetadataForEpoch(
	ctx context.Context,
	connection domain.GatewayConnection,
	m domain.GatewayControlMetadata,
	at int64,
) (bool, error) {
	if connection.ConnectionEpoch == 0 || connection.GatewayID != m.GatewayID {
		return false, fmt.Errorf("store: gateway connection metadata id mismatch")
	}
	if len(m.Capabilities) > 0 && !json.Valid(m.Capabilities) {
		return false, fmt.Errorf("store: invalid gateway capabilities")
	}
	n, err := r.q.UpdateGatewayConnectionMetadataForEpoch(ctx, storedb.UpdateGatewayConnectionMetadataForEpochParams{
		GrpcEndpoint:    nullString(m.GRPCEndpoint),
		SoftwareVersion: nullString(m.SoftwareVersion),
		Capabilities:    json.RawMessage(m.Capabilities),
		AppliedRevision: m.AppliedRevision,
		LastSeenAt:      sql.NullInt64{Int64: at, Valid: true},
		UpdatedAt:       at,
		ID:              connection.GatewayID,
		ConnectionEpoch: connection.ConnectionEpoch,
	})
	if err != nil {
		return false, fmt.Errorf("store: fenced gateway connection metadata: %w", err)
	}
	return r.fencedWriteApplied(ctx, n, connection)
}

func (r *GatewayRepo) fencedWriteApplied(
	ctx context.Context,
	changed int64,
	connection domain.GatewayConnection,
) (bool, error) {
	if changed == 1 {
		return true, nil
	}
	current, err := r.q.IsGatewayConnectionCurrent(ctx, storedb.IsGatewayConnectionCurrentParams{
		ID: connection.GatewayID, ConnectionEpoch: connection.ConnectionEpoch,
	})
	if err != nil {
		return false, fmt.Errorf("store: verify gateway connection epoch: %w", err)
	}
	return current, nil
}

func gatewayReportedStatus(status domain.GatewayStatus) bool {
	switch status {
	case domain.GatewayJoining,
		domain.GatewayActive,
		domain.GatewayDraining,
		domain.GatewayDrained,
		domain.GatewayDegraded:
		return true
	default:
		return false
	}
}

// Get fetches a gateway by id. Maps no-rows to not_found.
func (r *GatewayRepo) Get(ctx context.Context, id string) (domain.Gateway, error) {
	row, err := r.q.GetGateway(ctx, storedb.GetGatewayParams{ID: id})
	if err != nil {
		return domain.Gateway{}, notFound(err, "gateway")
	}
	return gatewayFromRow(row), nil
}

// List returns every non-deleted gateway with the complete, non-secret registry
// metadata required by an operator read model.
func (r *GatewayRepo) List(ctx context.Context) ([]domain.Gateway, error) {
	rows, err := r.q.ListGateways(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: list gateways: %w", err)
	}
	out := make([]domain.Gateway, 0, len(rows))
	for _, row := range rows {
		out = append(out, gatewayFromListRow(row))
	}
	return out, nil
}

// ListCertificateSummaries returns certificate metadata without selecting PEM,
// CSR, trust-bundle, or enrollment-token material.
func (r *GatewayRepo) ListCertificateSummaries(
	ctx context.Context,
	gatewayID string,
) ([]domain.GatewayCertificateSummary, error) {
	rows, err := r.q.ListGatewayCertificateSummaries(ctx, storedb.ListGatewayCertificateSummariesParams{
		GatewayID: gatewayID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: list gateway certificate summaries: %w", err)
	}
	out := make([]domain.GatewayCertificateSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.GatewayCertificateSummary{
			ID:               row.ID,
			AuthorityID:      row.AuthorityID,
			SerialNumber:     row.SerialNumber,
			Fingerprint:      append([]byte(nil), row.CertificateFingerprint...),
			NotBefore:        row.NotBefore,
			NotAfter:         row.NotAfter,
			CreatedAt:        row.CreatedAt,
			RevokedAt:        int64PtrFromNull(row.RevokedAt),
			RevocationReason: stringPtrFromNull(row.RevocationReason),
		})
	}
	return out, nil
}

// ListReconciliationResults returns the latest complete non-secret device
// reconciliation projection. It intentionally does not join sessions, so an
// unexpected local device cannot be presented as an invented session or org.
func (r *GatewayRepo) ListReconciliationResults(
	ctx context.Context,
	gatewayID string,
) ([]domain.GatewayReconciliationResult, error) {
	rows, err := r.q.ListGatewayReconciliationResults(ctx, storedb.ListGatewayReconciliationResultsParams{
		GatewayID: gatewayID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: list gateway reconciliation results: %w", err)
	}
	out := make([]domain.GatewayReconciliationResult, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.GatewayReconciliationResult{
			DeviceJID:       row.DeviceJid.String,
			SessionID:       stringPtrFromNull(row.SessionID),
			AssignmentEpoch: row.AssignmentEpoch,
			Status:          string(row.Status),
			DesiredRevision: row.DesiredRevision,
			UpdatedAt:       row.UpdatedAt,
		})
	}
	return out, nil
}

func (r *GatewayRepo) ResolveSessionEngineTarget(
	ctx context.Context,
	organizationID, sessionID string,
) (domain.SessionEngineTarget, error) {
	row, err := r.q.ResolveSessionEngineTarget(ctx, storedb.ResolveSessionEngineTargetParams{
		ID:             sessionID,
		OrganizationID: organizationID,
	})
	if err != nil {
		return domain.SessionEngineTarget{}, notFound(err, "session engine target")
	}
	return domain.SessionEngineTarget{
		SessionID:       row.SessionID,
		OrganizationID:  row.OrganizationID,
		GatewayID:       row.GatewayID,
		GRPCEndpoint:    row.GrpcEndpoint.String,
		AssignmentEpoch: row.AssignmentEpoch,
		ConnectionEpoch: row.ConnectionEpoch,
	}, nil
}

// Heartbeat refreshes the liveness signal the router prunes stale gateways by:
// last_seen_at and the current session_count, without rewriting the rest of the
// row. The gateway calls this on a timer (D8).
func (r *GatewayRepo) Heartbeat(ctx context.Context, id string, at int64, sessionCount int) error {
	err := r.q.GatewayHeartbeat(ctx, storedb.GatewayHeartbeatParams{
		LastSeenAt:   sql.NullInt64{Int64: at, Valid: true},
		SessionCount: uint32(sessionCount),
		UpdatedAt:    at,
		ID:           id,
	})
	if err != nil {
		return fmt.Errorf("store: gateway heartbeat: %w", err)
	}
	return nil
}

// SetStatus flips a gateway's lifecycle status (e.g. active→draining on SIGTERM,
// draining→drained once in-flight work finishes) and touches updated_at.
func (r *GatewayRepo) SetStatus(ctx context.Context, id string, status domain.GatewayStatus, at int64) error {
	err := r.q.SetGatewayStatus(ctx, storedb.SetGatewayStatusParams{
		Status:    storedb.GatewaysStatus(status),
		UpdatedAt: at,
		ID:        id,
	})
	if err != nil {
		return fmt.Errorf("store: set gateway status: %w", err)
	}
	return nil
}

// SetDesiredLifecycle is the administrative mutation for control-stream intent.
// Runtime and legacy status reports intentionally use SetStatus instead.
func (r *GatewayRepo) SetDesiredLifecycle(ctx context.Context, id, desired string, at int64) (bool, error) {
	if desired != "run" && desired != "drain" {
		return false, fmt.Errorf("store: invalid gateway desired lifecycle")
	}
	n, err := r.q.SetGatewayDesiredLifecycle(ctx, storedb.SetGatewayDesiredLifecycleParams{
		DesiredLifecycle: storedb.GatewaysDesiredLifecycle(desired), UpdatedAt: at, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("store: set gateway desired lifecycle: %w", err)
	}
	return n == 1, nil
}

// ListActive returns every gateway whose status is `active`, least-loaded first.
// The router uses it to enumerate placement candidates and for observability.
func (r *GatewayRepo) ListActive(ctx context.Context) ([]domain.Gateway, error) {
	rows, err := r.q.ListActiveGateways(ctx, storedb.ListActiveGatewaysParams{Status: storedb.GatewaysStatusActive})
	if err != nil {
		return nil, fmt.Errorf("store: list active gateways: %w", err)
	}
	out := make([]domain.Gateway, 0, len(rows))
	for _, row := range rows {
		out = append(out, gatewayFromListActiveRow(row))
	}
	return out, nil
}

// PickForPlacement returns the least-loaded `active` gateway that still has
// headroom (capacity IS NULL, i.e. unbounded, or session_count < capacity),
// preferring the freshest heartbeat among equally-loaded candidates. It maps
// "no candidate" to a not_found APIError so the create-session path can surface a
// clear 503 rather than silently hanging.
func (r *GatewayRepo) PickForPlacement(ctx context.Context) (domain.Gateway, error) {
	row, err := r.q.PickGatewayForPlacement(ctx, storedb.PickGatewayForPlacementParams{
		Status: storedb.GatewaysStatusActive,
	})
	if err != nil {
		return domain.Gateway{}, notFound(err, "placement gateway")
	}
	return gatewayFromPlacementRow(row), nil
}
