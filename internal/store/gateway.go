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
		ID:           row.ID,
		Label:        stringPtrFromNull(row.Label),
		Status:       domain.GatewayStatus(row.Status),
		SessionCount: int(row.SessionCount),
		Capacity:     intPtrFromNull32(row.Capacity),
		BaseURL:      stringPtrFromNull(row.BaseUrl),
		LastSeenAt:   int64PtrFromNull(row.LastSeenAt),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func gatewayFromListActiveRow(row storedb.ListActiveGatewaysRow) domain.Gateway {
	return gatewayFromRow(storedb.GetGatewayRow(row))
}

func gatewayFromPlacementRow(row storedb.PickGatewayForPlacementRow) domain.Gateway {
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
func (r *GatewayRepo) CreatePending(ctx context.Context, g domain.Gateway, notes, creatorID *string, desiredRevision uint64) error {
	if creatorID == nil || *creatorID == "" {
		return fmt.Errorf("store: gateway creator is required")
	}
	return r.q.CreateGateway(ctx, storedb.CreateGatewayParams{ID: g.ID, Label: nullString(g.Label), Notes: nullString(notes), Status: storedb.GatewaysStatusPendingEnrollment, CreatedByUserID: nullString(creatorID), Capacity: nullInt32(g.Capacity), DesiredRevision: desiredRevision, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt})
}
func (r *GatewayRepo) markEnrolledJoining(ctx context.Context, id string, at int64) (bool, error) {
	n, e := r.q.EnrollPendingGateway(ctx, storedb.EnrollPendingGatewayParams{EnrolledAt: sql.NullInt64{Int64: at, Valid: true}, UpdatedAt: at, ID: id})
	return n == 1, e
}

func (r *GatewayRepo) UpdateMetadata(ctx context.Context, id string, label, notes *string, capacity *int, revision uint64, at int64) (bool, error) {
	n, err := r.q.UpdateGatewayMetadata(ctx, storedb.UpdateGatewayMetadataParams{Label: nullString(label), Notes: nullString(notes), Capacity: nullInt32(capacity), DesiredRevision: revision, UpdatedAt: at, ID: id})
	return n == 1, err
}

func (r *GatewayRepo) Disable(ctx context.Context, id string, at int64) (bool, error) {
	n, err := r.q.DisableGateway(ctx, storedb.DisableGatewayParams{UpdatedAt: at, ID: id})
	return n == 1, err
}

// SoftDelete retains enrollment/certificate history and only hides a safely
// quiesced gateway with no live credential material.
func (r *GatewayRepo) SoftDelete(ctx context.Context, id string, at int64) (bool, error) {
	n, err := r.q.SoftDeleteGateway(ctx, storedb.SoftDeleteGatewayParams{DeletedAt: sql.NullInt64{Int64: at, Valid: true}, UpdatedAt: at, ID: id})
	return n == 1, err
}

func (r *GatewayRepo) UpdateConnectionMetadata(ctx context.Context, m domain.GatewayControlMetadata, at int64) (bool, error) {
	if len(m.Capabilities) > 0 && !json.Valid(m.Capabilities) {
		return false, fmt.Errorf("store: invalid gateway capabilities")
	}
	n, err := r.q.UpdateGatewayConnectionMetadata(ctx, storedb.UpdateGatewayConnectionMetadataParams{GrpcEndpoint: nullString(m.GRPCEndpoint), SoftwareVersion: nullString(m.SoftwareVersion), Capabilities: json.RawMessage(m.Capabilities), AppliedRevision: m.AppliedRevision, ConnectedAt: nullInt64(m.ConnectedAt), UpdatedAt: at, ID: m.GatewayID})
	return n == 1, err
}

// AcceptConnection atomically claims the next stream incarnation and persists
// its initial report for an enrolled, enabled gateway.
func (r *GatewayRepo) AcceptConnection(ctx context.Context, hello domain.GatewayConnectionHello, at int64) (domain.GatewayAcceptedConnection, error) {
	if hello.GatewayID == "" || hello.SessionCount < 0 || !gatewayReportedStatus(hello.Status) || (len(hello.Capabilities) > 0 && !json.Valid(hello.Capabilities)) {
		return domain.GatewayAcceptedConnection{}, fmt.Errorf("store: invalid gateway connection hello")
	}
	for range connectionEpochAllocationAttempts {
		current, err := r.q.GetGatewayConnectionEpochForAllocation(ctx, storedb.GetGatewayConnectionEpochForAllocationParams{ID: hello.GatewayID})
		if err != nil {
			return domain.GatewayAcceptedConnection{}, notFound(err, "connectable gateway")
		}
		n, err := r.q.AllocateGatewayConnectionEpoch(ctx, storedb.AllocateGatewayConnectionEpochParams{
			ConnectedAt:  sql.NullInt64{Int64: at, Valid: true},
			LastSeenAt:   sql.NullInt64{Int64: at, Valid: true},
			GrpcEndpoint: nullString(hello.GRPCEndpoint), SoftwareVersion: nullString(hello.SoftwareVersion),
			Capabilities: json.RawMessage(hello.Capabilities), SessionCount: uint32(hello.SessionCount),
			DrainCompleted: boolInt64(hello.Status == domain.GatewayDrained),
			ReportedStatus: storedb.GatewaysStatus(hello.Status), UpdatedAt: at, ID: hello.GatewayID, ConnectionEpoch: current,
		})
		if err != nil {
			return domain.GatewayAcceptedConnection{}, fmt.Errorf("store: allocate gateway connection epoch: %w", err)
		}
		if n == 1 {
			epoch := current + 1
			status, err := r.q.GetAcceptedGatewayConnectionStatus(ctx, storedb.GetAcceptedGatewayConnectionStatusParams{
				ID: hello.GatewayID, ConnectionEpoch: epoch,
			})
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return domain.GatewayAcceptedConnection{}, domain.ErrConflict("gateway connection was superseded during acceptance")
				}
				return domain.GatewayAcceptedConnection{}, fmt.Errorf("store: read accepted gateway connection: %w", err)
			}
			return domain.GatewayAcceptedConnection{ConnectionEpoch: epoch, Status: domain.GatewayStatus(status)}, nil
		}
	}
	return domain.GatewayAcceptedConnection{}, fmt.Errorf("store: allocate gateway connection epoch: concurrent allocation contention")
}

func (r *GatewayRepo) HeartbeatForEpoch(ctx context.Context, h domain.GatewayHeartbeat, at int64) (bool, error) {
	if h.ConnectionEpoch == 0 || h.SessionCount < 0 || !gatewayReportedStatus(h.Status) {
		return false, fmt.Errorf("store: invalid fenced gateway heartbeat")
	}
	n, err := r.q.GatewayHeartbeatForEpoch(ctx, storedb.GatewayHeartbeatForEpochParams{
		LastSeenAt: sql.NullInt64{Int64: at, Valid: true}, SessionCount: uint32(h.SessionCount),
		DrainCompleted: boolInt64(h.Status == domain.GatewayDrained),
		ReportedStatus: storedb.GatewaysStatus(h.Status), UpdatedAt: at, ID: h.GatewayID,
		ConnectionEpoch: h.ConnectionEpoch,
	})
	if err != nil {
		return false, fmt.Errorf("store: fenced gateway heartbeat: %w", err)
	}
	return r.fencedWriteApplied(ctx, n, h.GatewayConnection)
}

func (r *GatewayRepo) SetStatusForEpoch(ctx context.Context, report domain.GatewayLifecycleReport, at int64) (bool, error) {
	if report.ConnectionEpoch == 0 || !gatewayReportedStatus(report.Status) {
		return false, fmt.Errorf("store: invalid fenced gateway lifecycle")
	}
	n, err := r.q.SetGatewayStatusForEpoch(ctx, storedb.SetGatewayStatusForEpochParams{
		DrainCompleted: boolInt64(report.Status == domain.GatewayDrained),
		ReportedStatus: storedb.GatewaysStatus(report.Status), UpdatedAt: at,
		ID: report.GatewayID, ConnectionEpoch: report.ConnectionEpoch,
	})
	if err != nil {
		return false, fmt.Errorf("store: fenced gateway lifecycle: %w", err)
	}
	return r.fencedWriteApplied(ctx, n, report.GatewayConnection)
}

func (r *GatewayRepo) UpdateConnectionMetadataForEpoch(ctx context.Context, connection domain.GatewayConnection, m domain.GatewayControlMetadata, at int64) (bool, error) {
	if connection.ConnectionEpoch == 0 || connection.GatewayID != m.GatewayID {
		return false, fmt.Errorf("store: gateway connection metadata id mismatch")
	}
	if len(m.Capabilities) > 0 && !json.Valid(m.Capabilities) {
		return false, fmt.Errorf("store: invalid gateway capabilities")
	}
	n, err := r.q.UpdateGatewayConnectionMetadataForEpoch(ctx, storedb.UpdateGatewayConnectionMetadataForEpochParams{
		GrpcEndpoint: nullString(m.GRPCEndpoint), SoftwareVersion: nullString(m.SoftwareVersion),
		Capabilities: json.RawMessage(m.Capabilities), AppliedRevision: m.AppliedRevision,
		LastSeenAt: sql.NullInt64{Int64: at, Valid: true}, UpdatedAt: at,
		ID: connection.GatewayID, ConnectionEpoch: connection.ConnectionEpoch,
	})
	if err != nil {
		return false, fmt.Errorf("store: fenced gateway connection metadata: %w", err)
	}
	return r.fencedWriteApplied(ctx, n, connection)
}

func (r *GatewayRepo) fencedWriteApplied(ctx context.Context, changed int64, connection domain.GatewayConnection) (bool, error) {
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

func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func gatewayReportedStatus(status domain.GatewayStatus) bool {
	switch status {
	case domain.GatewayJoining, domain.GatewayActive, domain.GatewayDraining, domain.GatewayDrained, domain.GatewayDegraded:
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
	row, err := r.q.PickGatewayForPlacement(ctx, storedb.PickGatewayForPlacementParams{Status: storedb.GatewaysStatusActive})
	if err != nil {
		return domain.Gateway{}, notFound(err, "placement gateway")
	}
	return gatewayFromPlacementRow(row), nil
}
