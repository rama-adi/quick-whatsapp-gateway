package store

import (
	"context"
	"database/sql"
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
		Status:       string(g.Status),
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
		Status:    string(status),
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
	rows, err := r.q.ListActiveGateways(ctx, storedb.ListActiveGatewaysParams{Status: string(domain.GatewayActive)})
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
	row, err := r.q.PickGatewayForPlacement(ctx, storedb.PickGatewayForPlacementParams{Status: string(domain.GatewayActive)})
	if err != nil {
		return domain.Gateway{}, notFound(err, "placement gateway")
	}
	return gatewayFromPlacementRow(row), nil
}
