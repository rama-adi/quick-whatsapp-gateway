package store

import (
	"context"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// GatewayRepo is the repository for the gateways registry (§7). In v2 there is one
// self-row (this gateway's GATEWAY_ID); more rows appear when sharding so that
// sessions can be pinned to the gateway that holds their keystore.
type GatewayRepo struct {
	db dbExecQuerier
}

// NewGatewayRepo constructs a GatewayRepo.
func NewGatewayRepo(db dbExecQuerier) *GatewayRepo { return &GatewayRepo{db: db} }

const gatewayCols = "id, label, base_url, last_seen_at, created_at, updated_at"

func scanGateway(s rowScanner) (domain.Gateway, error) {
	var g domain.Gateway
	if err := s.Scan(&g.ID, &g.Label, &g.BaseURL, &g.LastSeenAt, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return domain.Gateway{}, err
	}
	return g, nil
}

// Upsert inserts or updates this gateway's registry row by id (= GATEWAY_ID).
// created_at is preserved on update; the mutable fields and updated_at refresh.
func (r *GatewayRepo) Upsert(ctx context.Context, g domain.Gateway) error {
	const q = `INSERT INTO gateways (id, label, base_url, last_seen_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE label=VALUES(label), base_url=VALUES(base_url),
	last_seen_at=VALUES(last_seen_at), updated_at=VALUES(updated_at)`
	if _, err := r.db.ExecContext(ctx, q, g.ID, g.Label, g.BaseURL, g.LastSeenAt, g.CreatedAt, g.UpdatedAt); err != nil {
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

// TouchLastSeen updates a gateway's last_seen_at heartbeat without rewriting the
// rest of the row.
func (r *GatewayRepo) TouchLastSeen(ctx context.Context, id string, at int64) error {
	const q = "UPDATE gateways SET last_seen_at=?, updated_at=? WHERE id=?"
	if _, err := r.db.ExecContext(ctx, q, at, at, id); err != nil {
		return fmt.Errorf("store: touch gateway last_seen: %w", err)
	}
	return nil
}
