package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// GatewayRepo is the repository for the gateways registry (§7) — the routing table
// the central router reads to place sessions and proxy requests. Each row is a
// gateway's self-reported identity, reachability (base_url), lifecycle status, and
// load (session_count/capacity). The owning gateway writes its own row
// (Register/Heartbeat/SetStatus); the router reads the table (Get/ListActive/
// PickForPlacement).
type GatewayRepo struct {
	db dbExecQuerier
}

// NewGatewayRepo constructs a GatewayRepo.
func NewGatewayRepo(db dbExecQuerier) *GatewayRepo { return &GatewayRepo{db: db} }

const gatewayCols = "id, label, status, session_count, capacity, base_url, last_seen_at, created_at, updated_at"

func scanGateway(s rowScanner) (domain.Gateway, error) {
	var g domain.Gateway
	if err := s.Scan(&g.ID, &g.Label, &g.Status, &g.SessionCount, &g.Capacity, &g.BaseURL, &g.LastSeenAt, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return domain.Gateway{}, err
	}
	return g, nil
}

// Upsert inserts or updates this gateway's registry row by id (= GATEWAY_ID).
// created_at is preserved on update; the mutable fields and updated_at refresh.
// This is the boot self-registration path (status transitions to joining→active);
// the heartbeat (Heartbeat) maintains last_seen_at + session_count thereafter, and
// is intentionally NOT clobbered here so a heartbeat racing a re-register is safe.
func (r *GatewayRepo) Upsert(ctx context.Context, g domain.Gateway) error {
	const q = `INSERT INTO gateways (id, label, status, session_count, capacity, base_url, last_seen_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE label=VALUES(label), status=VALUES(status),
	capacity=VALUES(capacity), base_url=VALUES(base_url),
	last_seen_at=VALUES(last_seen_at), updated_at=VALUES(updated_at)`
	if _, err := r.db.ExecContext(ctx, q,
		g.ID, g.Label, g.Status, g.SessionCount, g.Capacity, g.BaseURL, g.LastSeenAt, g.CreatedAt, g.UpdatedAt,
	); err != nil {
		return fmt.Errorf("store: upsert gateway: %w", err)
	}
	return nil
}

// Get fetches a gateway by id. Maps no-rows to not_found.
func (r *GatewayRepo) Get(ctx context.Context, id string) (domain.Gateway, error) {
	q := "SELECT " + gatewayCols + " FROM gateways WHERE id = ?"
	g, err := scanGateway(r.db.QueryRowContext(ctx, q, id))
	if err != nil {
		return domain.Gateway{}, notFound(err, "gateway")
	}
	return g, nil
}

// Heartbeat refreshes the liveness signal the router prunes stale gateways by:
// last_seen_at and the current session_count, without rewriting the rest of the
// row. The gateway calls this on a timer (D8).
func (r *GatewayRepo) Heartbeat(ctx context.Context, id string, at int64, sessionCount int) error {
	const q = "UPDATE gateways SET last_seen_at=?, session_count=?, updated_at=? WHERE id=?"
	if _, err := r.db.ExecContext(ctx, q, at, sessionCount, at, id); err != nil {
		return fmt.Errorf("store: gateway heartbeat: %w", err)
	}
	return nil
}

// SetStatus flips a gateway's lifecycle status (e.g. active→draining on SIGTERM,
// draining→drained once in-flight work finishes) and touches updated_at.
func (r *GatewayRepo) SetStatus(ctx context.Context, id string, status domain.GatewayStatus, at int64) error {
	const q = "UPDATE gateways SET status=?, updated_at=? WHERE id=?"
	if _, err := r.db.ExecContext(ctx, q, status, at, id); err != nil {
		return fmt.Errorf("store: set gateway status: %w", err)
	}
	return nil
}

// ListActive returns every gateway whose status is `active`, least-loaded first.
// The router uses it to enumerate placement candidates and for observability.
func (r *GatewayRepo) ListActive(ctx context.Context) ([]domain.Gateway, error) {
	q := "SELECT " + gatewayCols + " FROM gateways WHERE status = ? ORDER BY session_count ASC, id ASC"
	rows, err := r.db.QueryContext(ctx, q, domain.GatewayActive)
	if err != nil {
		return nil, fmt.Errorf("store: list active gateways: %w", err)
	}
	defer rows.Close()
	var out []domain.Gateway
	for rows.Next() {
		g, err := scanGateway(rows)
		if err != nil {
			return nil, scanErr("gateways", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// PickForPlacement returns the least-loaded `active` gateway that still has
// headroom (capacity IS NULL, i.e. unbounded, or session_count < capacity),
// preferring the freshest heartbeat among equally-loaded candidates. It maps
// "no candidate" to a not_found APIError so the create-session path can surface a
// clear 503 rather than silently hanging.
func (r *GatewayRepo) PickForPlacement(ctx context.Context) (domain.Gateway, error) {
	q := "SELECT " + gatewayCols + ` FROM gateways
WHERE status = ? AND (capacity IS NULL OR session_count < capacity)
ORDER BY session_count ASC, last_seen_at DESC, id ASC
LIMIT 1`
	g, err := scanGateway(r.db.QueryRowContext(ctx, q, domain.GatewayActive))
	if err != nil {
		return domain.Gateway{}, notFound(err, "placement gateway")
	}
	return g, nil
}
